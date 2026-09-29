package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"novera/internal/buildinfo"
)

// Shell exposes host/OS interactions that require the running application
// instance (native dialogs, opening external resources). It is intentionally
// thin — file logic lives in the workspace service.
type Shell struct {
	unsavedResources atomic.Bool
	nativeClose      nativeCloseGate
}

const (
	nativeCloseRequestTimeout       = 5 * time.Second
	nativeCloseAuthorizationTimeout = 2 * time.Second
	nativeCloseReplayPasses         = 2
)

// nativeCloseGate is the native half of the close-decision handshake. The
// renderer's asynchronously mirrored dirty bit is deliberately not consulted:
// every fresh native close is cancelled until the renderer answers a nonce
// from its current Zustand snapshot. A matching clean answer grants only a
// short replay window; missing, malformed, stale, and dirty answers fail closed.
type nativeCloseGate struct {
	mu                   sync.Mutex
	now                  func() time.Time
	newNonce             func() (string, error)
	requestTimeout       time.Duration
	authorizationTimeout time.Duration
	pendingNonce         string
	pendingUntil         time.Time
	authorizedUntil      time.Time
	authorizedPasses     uint8
}

type nativeCloseRequest struct {
	Nonce string `json:"nonce"`
}

type nativeCloseResolution uint8

const (
	nativeCloseStale nativeCloseResolution = iota
	nativeCloseBlocked
	nativeCloseAuthorized
)

func secureNativeCloseNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (g *nativeCloseGate) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *nativeCloseGate) requestTTL() time.Duration {
	if g.requestTimeout > 0 {
		return g.requestTimeout
	}
	return nativeCloseRequestTimeout
}

func (g *nativeCloseGate) authorizationTTL() time.Duration {
	if g.authorizationTimeout > 0 {
		return g.authorizationTimeout
	}
	return nativeCloseAuthorizationTimeout
}

func (g *nativeCloseGate) makeNonce() (string, error) {
	if g.newNonce != nil {
		return g.newNonce()
	}
	return secureNativeCloseNonce()
}

// requestClose returns true only while replaying a recently authorized close.
// A nil request means nonce creation failed; callers must still cancel the
// native close, but have nothing safe to send to the renderer.
func (g *nativeCloseGate) requestClose() (bool, *nativeCloseRequest) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.clock()
	if g.authorizedPasses > 0 && now.Before(g.authorizedUntil) {
		g.authorizedPasses--
		if g.authorizedPasses == 0 {
			g.authorizedUntil = time.Time{}
		}
		return true, nil
	}
	g.authorizedUntil = time.Time{}
	g.authorizedPasses = 0

	if g.pendingNonce != "" && now.Before(g.pendingUntil) {
		// The original native close is already cancelled and its renderer
		// decision is still in flight. Do not create duplicate responders.
		return false, nil
	}
	g.pendingNonce = ""
	g.pendingUntil = time.Time{}

	nonce, err := g.makeNonce()
	if err != nil || nonce == "" {
		return false, nil
	}
	g.pendingNonce = nonce
	g.pendingUntil = now.Add(g.requestTTL())
	return false, &nativeCloseRequest{Nonce: nonce}
}

func (g *nativeCloseGate) resolve(nonce string, hasUnsavedResources bool) nativeCloseResolution {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.clock()
	if g.pendingNonce == "" || nonce == "" || nonce != g.pendingNonce || !now.Before(g.pendingUntil) {
		// A stale answer must not disturb a newer in-flight request.
		if g.pendingNonce != "" && !now.Before(g.pendingUntil) {
			g.pendingNonce = ""
			g.pendingUntil = time.Time{}
		}
		return nativeCloseStale
	}

	g.pendingNonce = ""
	g.pendingUntil = time.Time{}
	if hasUnsavedResources {
		return nativeCloseBlocked
	}
	g.authorizedUntil = now.Add(g.authorizationTTL())
	// App.Quit first traverses ShouldQuit, then App.cleanup closes the main
	// window and traverses WindowClosing. Bound the permit to those two native
	// gates as well as a short deadline so it cannot become a general bypass.
	g.authorizedPasses = nativeCloseReplayPasses
	return nativeCloseAuthorized
}

func (s *Shell) requestNativeClose() (bool, *nativeCloseRequest) {
	if s == nil {
		return false, nil
	}
	return s.nativeClose.requestClose()
}

func (s *Shell) resolveNativeClose(nonce string, hasUnsavedResources bool) nativeCloseResolution {
	if s == nil {
		return nativeCloseStale
	}
	return s.nativeClose.resolve(nonce, hasUnsavedResources)
}

// parseNativeCloseDecision accepts only the JSON shapes produced by the Wails
// event bridge. In particular, it never coerces strings/numbers to booleans.
func parseNativeCloseDecision(data any) (nonce string, hasUnsavedResources bool, ok bool) {
	if wrapped, wrappedOK := data.([]any); wrappedOK {
		if len(wrapped) != 1 {
			return "", false, false
		}
		return parseNativeCloseDecision(wrapped[0])
	}
	record, recordOK := data.(map[string]any)
	if !recordOK {
		return "", false, false
	}
	nonce, nonceOK := record["nonce"].(string)
	hasUnsavedResources, dirtyOK := record["hasUnsavedResources"].(bool)
	if !nonceOK || nonce == "" || !dirtyOK {
		return "", false, false
	}
	return nonce, hasUnsavedResources, true
}

// SetUnsavedResources mirrors the renderer's aggregate dirty-resource state
// for native diagnostics. Native close authorization does not trust this
// asynchronous mirror; it uses the nonce-based live renderer handshake above.
func (s *Shell) SetUnsavedResources(unsaved bool) {
	s.unsavedResources.Store(unsaved)
}

func (s *Shell) hasUnsavedResources() bool {
	return s != nil && s.unsavedResources.Load()
}

// BuildInfo returns the canonical, non-secret identity embedded in the running
// executable so users and support can identify the exact build in About.
func (s *Shell) BuildInfo() buildinfo.Info {
	return buildinfo.Current()
}

// SelectFolder opens a native directory picker and returns the chosen absolute
// path. An empty string means the user cancelled.
func (s *Shell) SelectFolder() (string, error) {
	dir, err := application.Get().Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		SetTitle("Open Folder").
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return dir, nil
}

// SaveTextFile opens a native Save dialog seeded with suggestedName and writes
// content to the chosen path. Returns the path, or "" if the user cancelled.
func (s *Shell) SaveTextFile(suggestedName, content string) (string, error) {
	path, err := application.Get().Dialog.SaveFile().
		SetFilename(suggestedName).
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if err := writeTextAtomic(path, []byte(content)); err != nil {
		return "", err
	}
	return path, nil
}

// writeTextAtomic stages a user-selected text export beside its destination,
// syncs the complete bytes, and publishes only after every write succeeds. A
// failed save therefore leaves an existing destination intact. When replacing
// a regular file, retain its permission bits instead of silently resetting it.
func writeTextAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	perm := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular destination %q", path)
		}
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".novera-export-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		_ = os.Remove(tmp)
	}()
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	closed = true
	if err := replaceExportFile(tmp, path); err != nil {
		return err
	}
	return syncExportDirectory(dir, path)
}

// OpenExternal opens a web/mail URL in the OS default handler. Only http(s) and
// mailto are allowed; file/UNC and other schemes are rejected so a crafted
// target can't trigger SMB/NTLM auth or launch arbitrary local handlers.
func (s *Shell) OpenExternal(target string) error {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "\\\\") || strings.HasPrefix(target, "//") {
		return fmt.Errorf("refusing to open UNC path")
	}
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return application.Get().Browser.OpenURL(target)
	case "mailto":
		// Rebuild the mailto from scratch with only safe headers so params like
		// attach/attachment (which some legacy handlers honour to pre-attach a
		// local file) can't ride along.
		return application.Get().Browser.OpenURL(safeMailto(u))
	default:
		return fmt.Errorf("refusing to open scheme %q", u.Scheme)
	}
}

// safeMailto reconstructs a mailto: URL keeping only the recipient(s) and an
// allowlist of headers (subject, body, cc, bcc); every other parameter —
// notably attach/attachment — is dropped.
func safeMailto(u *url.URL) string {
	out := &url.URL{Scheme: "mailto", Opaque: u.Opaque}
	in := u.Query()
	safe := url.Values{}
	for _, k := range []string{"subject", "body", "cc", "bcc"} {
		if v := in.Get(k); v != "" {
			safe.Set(k, v)
		}
	}
	if enc := safe.Encode(); enc != "" {
		out.RawQuery = enc
	}
	return out.String()
}
