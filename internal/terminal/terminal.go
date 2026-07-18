// Package terminal is the Wails service backing the integrated terminal. It
// runs a real interactive shell on a pseudo-terminal (ConPTY on Windows, a
// unix PTY elsewhere) and streams output to the frontend over Wails events.
// Output is base64-encoded so a chunk boundary that splits a multi-byte rune
// can never corrupt the stream.
package terminal

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Event names emitted to the frontend.
const (
	EventData = "term:data"
	EventExit = "term:exit"
)

// RootProvider yields the active workspace root (used as the shell's cwd).
type RootProvider interface{ Root() string }

type dataEvent struct {
	ID   string `json:"id"`
	Data string `json:"data"` // base64-encoded bytes
}

type exitEvent struct {
	ID       string `json:"id"`
	Code     int    `json:"code"`     // process exit code (-1 if killed by a signal/unknown)
	Signaled bool   `json:"signaled"` // true if the shell was terminated by a signal
}

type session struct {
	id        string
	pty       pty.Pty
	cmd       *pty.Cmd
	tree      *terminalProcessTree
	closeOnce sync.Once
}

// Service is the bound Wails terminal service.
type Service struct {
	mu       sync.Mutex
	roots    RootProvider
	sessions map[string]*session
	seq      int
}

// New constructs the terminal service.
func New(roots RootProvider) *Service {
	return &Service{roots: roots, sessions: map[string]*session{}}
}

// Start launches a shell on a new pseudo-terminal sized to cols×rows and returns
// the session id. Output arrives via the "term:data" event.
func (s *Service) Start(cols, rows int) (string, error) {
	root := strings.TrimSpace(s.root())
	if root == "" {
		return "", errors.New("open a folder before starting a terminal")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("terminal workspace root: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("terminal workspace root is not a directory")
	}
	p, err := pty.New()
	if err != nil {
		return "", err
	}
	if cols > 0 && rows > 0 {
		_ = p.Resize(cols, rows)
	}
	name, args := defaultShell()
	cmd := p.Command(name, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	prepareTerminalCommand(cmd)
	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return "", err
	}
	tree, err := ownTerminalProcessTree(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = p.Close()
		_ = cmd.Wait()
		return "", fmt.Errorf("terminal process-tree ownership: %w", err)
	}

	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("term-%d", s.seq)
	sess := &session{id: id, pty: p, cmd: cmd, tree: tree}
	s.sessions[id] = sess
	s.mu.Unlock()

	go s.readLoop(sess)
	// The process is reaped by shutdown()'s single cmd.Wait(); a separate reaper
	// goroutine would race that Wait against Kill (it could signal a reused PID).

	return id, nil
}

// ServiceShutdown tears down every live session when the app exits so child
// shells and their PTYs aren't leaked.
func (s *Service) ServiceShutdown() error {
	s.mu.Lock()
	live := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		live = append(live, sess)
	}
	s.mu.Unlock()
	for _, sess := range live {
		s.shutdown(sess, false)
	}
	return nil
}

// Write sends user input (keystrokes) to the session's shell. An unknown id is
// treated as an already-closed session (no-op, nil error) — the same contract as
// Resize and Close — so a late keystroke racing a restart isn't a hard error.
func (s *Service) Write(id, data string) error {
	sess := s.get(id)
	if sess == nil {
		return nil
	}
	_, err := sess.pty.Write([]byte(data))
	return err
}

// Resize updates the pseudo-terminal dimensions.
func (s *Service) Resize(id string, cols, rows int) error {
	sess := s.get(id)
	if sess == nil {
		return nil
	}
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return sess.pty.Resize(cols, rows)
}

// Close terminates a session (UI-initiated). No exit event is emitted — the UI
// already knows it closed the panel.
func (s *Service) Close(id string) error {
	sess := s.get(id)
	if sess == nil {
		return nil
	}
	s.shutdown(sess, false)
	return nil
}

// shutdown tears a session down exactly once. emitExit is true only when the
// shell itself exited (so the UI can show "[process exited]"); it is false for
// a UI-initiated Close.
func (s *Service) shutdown(sess *session, emitExit bool) {
	sess.closeOnce.Do(func() {
		s.remove(sess.id)
		if sess.tree != nil {
			if err := sess.tree.kill(); err != nil && sess.cmd.Process != nil {
				_ = sess.cmd.Process.Kill()
			}
		} else if sess.cmd.Process != nil {
			_ = sess.cmd.Process.Kill()
		}
		_ = sess.pty.Close()
		// Reap exactly here (once, guarded by closeOnce) so there's no separate
		// goroutine racing Wait against the Kill above.
		waitErr := sess.cmd.Wait()
		if sess.tree != nil {
			_ = sess.tree.close()
		}
		if emitExit {
			// emitExit is true only on a natural shell exit (the Kill above was a
			// no-op for an already-gone process), so the Wait result reflects the
			// shell's own exit status — surface it so the UI can tell a clean exit
			// from a crash.
			code, signaled := exitInfo(waitErr)
			if app := application.Get(); app != nil {
				app.Event.Emit(EventExit, exitEvent{ID: sess.id, Code: code, Signaled: signaled})
			}
		}
	})
}

// exitInfo derives the process exit code (and whether it was signalled) from a
// cmd.Wait() error. A nil error is a clean exit (code 0); ExitCode() returns -1
// when the process was terminated by a signal.
func exitInfo(waitErr error) (code int, signaled bool) {
	if waitErr == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) && ee.ProcessState != nil {
		code = ee.ProcessState.ExitCode()
		return code, code == -1
	}
	return -1, false
}

func (s *Service) readLoop(sess *session) {
	// Coalesce PTY output: a separate emitter batches chunks and emits at most
	// ~every 16ms (or when ~256 KiB accumulates). The buffered channel applies
	// backpressure to the reader so a noisy process can't flood the bridge.
	dataCh := make(chan []byte, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(16 * time.Millisecond)
		defer ticker.Stop()
		var pending []byte
		flush := func() {
			if len(pending) == 0 {
				return
			}
			enc := base64.StdEncoding.EncodeToString(pending)
			if app := application.Get(); app != nil {
				app.Event.Emit(EventData, dataEvent{ID: sess.id, Data: enc})
			}
			pending = pending[:0]
		}
		for {
			select {
			case b, ok := <-dataCh:
				if !ok {
					flush()
					return
				}
				pending = append(pending, b...)
				if len(pending) >= 256*1024 {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()

	buf := make([]byte, 16*1024)
	for {
		n, err := sess.pty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			dataCh <- chunk // blocks if the emitter is behind → backpressure
		}
		if err != nil {
			break
		}
	}
	close(dataCh)
	<-done
	s.shutdown(sess, true)
}

func (s *Service) get(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Service) remove(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func (s *Service) root() string {
	if s.roots == nil {
		return ""
	}
	return s.roots.Root()
}

// defaultShell returns an ABSOLUTE shell path. ConPTY resolves a bare name
// relative to the cwd, so we must resolve via PATH (LookPath) and fall back to
// known absolute locations — otherwise starting a session in a workspace dir
// that has no powershell.exe fails.
func defaultShell() (string, []string) {
	if runtime.GOOS == "windows" {
		for _, name := range []string{"pwsh.exe", "powershell.exe"} {
			if p, err := exec.LookPath(name); err == nil {
				return p, nil
			}
		}
		if root := os.Getenv("SystemRoot"); root != "" {
			ps := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			if _, err := os.Stat(ps); err == nil {
				return ps, nil
			}
		}
		if comspec := os.Getenv("ComSpec"); comspec != "" {
			return comspec, nil
		}
		return `C:\Windows\System32\cmd.exe`, nil
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, nil
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p, nil
	}
	return "/bin/sh", nil
}
