package agent

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"novera/internal/settings"
	"novera/internal/workspace"
)

func TestBoundedCommandOutputCapsDuringConcurrentAcquisition(t *testing.T) {
	output := newBoundedCommandOutput(1_000)
	chunk := []byte(strings.Repeat("x", 10_000))
	var writers sync.WaitGroup
	for i := 0; i < 8; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			if n, err := output.Write(chunk); err != nil || n != len(chunk) {
				t.Errorf("Write = (%d, %v), want (%d, nil)", n, err, len(chunk))
			}
		}()
	}
	writers.Wait()
	text := output.String()
	if len(text) > 1_100 {
		t.Fatalf("retained output length = %d, want a bounded prefix plus marker", len(text))
	}
	if !strings.Contains(text, "79000 bytes omitted") {
		t.Fatalf("missing exact truncation accounting: %q", text[len(text)-80:])
	}
}

func commandTestService(t *testing.T) *Service {
	t.Helper()
	ws := workspace.New()
	if _, err := ws.Open(t.TempDir()); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	return &Service{
		ws: ws,
		settings: staticAgentSettings{value: settings.Settings{Agent: settings.Agent{
			MaxToolOutputChars: 1_000,
		}}},
	}
}

func TestRunCommandReturnsNonzeroExitAsErrorWithBoundedOutput(t *testing.T) {
	s := commandTestService(t)
	command := `printf 'visible-output'; exit 7`
	if runtime.GOOS == "windows" {
		command = `echo visible-output & exit /b 7`
	}
	output, err := s.runCommand(context.Background(), command, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "status 7") {
		t.Fatalf("runCommand error = %v, want status 7", err)
	}
	if !strings.Contains(output, "visible-output") {
		t.Fatalf("runCommand lost bounded failure output: %q", output)
	}
}

func TestRunCommandTimeoutTerminatesOwnedProcessTree(t *testing.T) {
	s := commandTestService(t)
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "ping -n 30 127.0.0.1 >nul"
	}
	started := time.Now()
	_, err := s.runCommand(context.Background(), command, 150*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("runCommand error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("process tree did not terminate promptly: %s", elapsed)
	}
}
