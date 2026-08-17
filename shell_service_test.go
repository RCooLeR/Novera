package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func deterministicNativeCloseGate(now *time.Time, nonces ...string) nativeCloseGate {
	next := 0
	return nativeCloseGate{
		now: func() time.Time { return *now },
		newNonce: func() (string, error) {
			if next >= len(nonces) {
				return "", errors.New("test nonce sequence exhausted")
			}
			nonce := nonces[next]
			next++
			return nonce, nil
		},
		requestTimeout:       5 * time.Second,
		authorizationTimeout: 2 * time.Second,
	}
}

func TestWriteTextAtomicReplacesCompleteDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.txt")
	if err := os.WriteFile(path, []byte("previous"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := writeTextAtomic(path, []byte("complete replacement")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "complete replacement" {
		t.Fatalf("destination = %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if gotPerm := info.Mode().Perm(); gotPerm != 0o751 {
			t.Fatalf("destination mode = %o, want 751", gotPerm)
		}
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".novera-export-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary exports left behind: %v", leftovers)
	}
}

func TestWriteTextAtomicRefusesDirectoryDestination(t *testing.T) {
	dir := t.TempDir()
	if err := writeTextAtomic(dir, []byte("must not publish")); err == nil {
		t.Fatal("expected non-regular destination refusal")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("destination directory was damaged: info=%v err=%v", info, err)
	}
}

func TestShellTracksUnsavedResourcesMirror(t *testing.T) {
	shell := &Shell{}
	if shell.hasUnsavedResources() {
		t.Fatal("new shell unexpectedly reports unsaved resources")
	}
	shell.SetUnsavedResources(true)
	if !shell.hasUnsavedResources() {
		t.Fatal("native diagnostic mirror did not retain dirty state")
	}
	shell.SetUnsavedResources(false)
	if shell.hasUnsavedResources() {
		t.Fatal("native diagnostic mirror did not clear dirty state")
	}
}

func TestNativeCloseGateFailsClosedBeforeDelayedDirtyMirrorArrives(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	shell := &Shell{nativeClose: deterministicNativeCloseGate(&now, "nonce-1", "nonce-2")}

	// The renderer has edited a resource, but its queued bridge report has not
	// reached Go yet. The stale mirror must never authorize this immediate close.
	shell.SetUnsavedResources(false)
	authorized, request := shell.requestNativeClose()
	if authorized || request == nil || request.Nonce != "nonce-1" {
		t.Fatalf("first close = authorized %v, request %#v; want cancelled nonce-1", authorized, request)
	}

	// The live renderer decision says dirty. A subsequently arriving mirror is
	// merely diagnostic and cannot change the decision.
	shell.SetUnsavedResources(true)
	if got := shell.resolveNativeClose("nonce-1", true); got != nativeCloseBlocked {
		t.Fatalf("dirty decision = %v, want blocked", got)
	}
	if authorized, request = shell.requestNativeClose(); authorized || request == nil || request.Nonce != "nonce-2" {
		t.Fatalf("close after dirty decision = authorized %v, request %#v; want a fresh cancelled request", authorized, request)
	}
}

func TestNativeCloseGateCleanDecisionPermitsOnlyImmediateReplay(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	shell := &Shell{nativeClose: deterministicNativeCloseGate(&now, "nonce-1", "nonce-2")}

	_, request := shell.requestNativeClose()
	if request == nil {
		t.Fatal("first close did not produce a renderer request")
	}
	if got := shell.resolveNativeClose(request.Nonce, false); got != nativeCloseAuthorized {
		t.Fatalf("clean decision = %v, want authorized", got)
	}
	if authorized, replayRequest := shell.requestNativeClose(); !authorized || replayRequest != nil {
		t.Fatalf("ShouldQuit replay = authorized %v, request %#v; want authorized", authorized, replayRequest)
	}
	if authorized, replayRequest := shell.requestNativeClose(); !authorized || replayRequest != nil {
		t.Fatalf("WindowClosing replay = authorized %v, request %#v; want authorized", authorized, replayRequest)
	}
	if authorized, nextRequest := shell.requestNativeClose(); authorized || nextRequest == nil || nextRequest.Nonce != "nonce-2" {
		t.Fatalf("third replay = authorized %v, request %#v; want fresh cancelled request", authorized, nextRequest)
	}
}

func TestNativeCloseGateUnusedReplayAuthorizationExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	shell := &Shell{nativeClose: deterministicNativeCloseGate(&now, "nonce-1", "nonce-2")}

	_, request := shell.requestNativeClose()
	if got := shell.resolveNativeClose(request.Nonce, false); got != nativeCloseAuthorized {
		t.Fatalf("clean decision = %v, want authorized", got)
	}
	now = now.Add(2 * time.Second)
	if authorized, nextRequest := shell.requestNativeClose(); authorized || nextRequest == nil || nextRequest.Nonce != "nonce-2" {
		t.Fatalf("expired unused replay = authorized %v, request %#v; want fresh cancelled request", authorized, nextRequest)
	}
}

func TestNativeCloseGateRejectsStaleNonceWithoutDisturbingCurrentRequest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	shell := &Shell{nativeClose: deterministicNativeCloseGate(&now, "nonce-old", "nonce-current")}

	_, oldRequest := shell.requestNativeClose()
	now = now.Add(6 * time.Second)
	_, currentRequest := shell.requestNativeClose()
	if oldRequest == nil || currentRequest == nil {
		t.Fatalf("requests = old %#v current %#v", oldRequest, currentRequest)
	}
	if got := shell.resolveNativeClose(oldRequest.Nonce, false); got != nativeCloseStale {
		t.Fatalf("old nonce decision = %v, want stale", got)
	}
	if authorized, duplicate := shell.requestNativeClose(); authorized || duplicate != nil {
		t.Fatalf("stale response disturbed current request: authorized %v request %#v", authorized, duplicate)
	}
	if got := shell.resolveNativeClose(currentRequest.Nonce, false); got != nativeCloseAuthorized {
		t.Fatalf("current nonce decision = %v, want authorized", got)
	}
}

func TestNativeCloseGateTimedOutDecisionCannotAuthorize(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	shell := &Shell{nativeClose: deterministicNativeCloseGate(&now, "nonce-expired", "nonce-retry")}

	_, request := shell.requestNativeClose()
	now = now.Add(5 * time.Second)
	if got := shell.resolveNativeClose(request.Nonce, false); got != nativeCloseStale {
		t.Fatalf("timed-out clean decision = %v, want stale", got)
	}
	if authorized, retry := shell.requestNativeClose(); authorized || retry == nil || retry.Nonce != "nonce-retry" {
		t.Fatalf("post-timeout close = authorized %v, request %#v; want fresh cancelled request", authorized, retry)
	}
}

func TestNativeCloseGateNonceFailureCancels(t *testing.T) {
	shell := &Shell{nativeClose: nativeCloseGate{
		newNonce: func() (string, error) { return "", errors.New("entropy unavailable") },
	}}
	if authorized, request := shell.requestNativeClose(); authorized || request != nil {
		t.Fatalf("nonce failure = authorized %v, request %#v; want fail-closed cancellation", authorized, request)
	}
}

func TestParseNativeCloseDecisionIsStrict(t *testing.T) {
	nonce, dirty, ok := parseNativeCloseDecision(map[string]any{
		"nonce":               "nonce-1",
		"hasUnsavedResources": true,
	})
	if !ok || nonce != "nonce-1" || !dirty {
		t.Fatalf("valid decision = nonce %q dirty %v ok %v", nonce, dirty, ok)
	}

	invalid := []any{
		nil,
		map[string]any{"nonce": "nonce-1"},
		map[string]any{"nonce": "nonce-1", "hasUnsavedResources": "false"},
		map[string]any{"nonce": 1, "hasUnsavedResources": false},
		[]any{},
		[]any{map[string]any{"nonce": "one", "hasUnsavedResources": false}, map[string]any{}},
	}
	for _, value := range invalid {
		if _, _, ok := parseNativeCloseDecision(value); ok {
			t.Errorf("accepted invalid decision %#v", value)
		}
	}
}
