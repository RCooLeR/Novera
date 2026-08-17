package terminal

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type fixedRoot string

func (root fixedRoot) Root() string { return string(root) }

type blockingRoot struct {
	path    string
	entered chan struct{}
	release chan struct{}
}

func (root *blockingRoot) Root() string {
	root.entered <- struct{}{}
	<-root.release
	return root.path
}

func TestStartRejectsMissingWorkspaceBeforeLaunchingShell(t *testing.T) {
	for _, root := range []fixedRoot{"", fixedRoot(filepath.Join(t.TempDir(), "missing"))} {
		s := New(root)
		if id, err := s.Start(80, 24); err == nil || id != "" {
			t.Fatalf("Start with root %q = (%q, %v), want a pre-launch error", root, id, err)
		}
		if len(s.sessions) != 0 {
			t.Fatal("failed terminal start registered a live session")
		}
	}
}

func TestServiceShutdownPermanentlyRejectsNewStarts(t *testing.T) {
	s := New(fixedRoot(t.TempDir()))
	if err := s.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Start(80, 24); !errors.Is(err, errServiceStopping) || id != "" {
		t.Fatalf("Start after shutdown = (%q, %v), want permanent stopping error", id, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopping || s.pendingStarts != 0 || len(s.sessions) != 0 {
		t.Fatalf("shutdown state = stopping %v pending %d sessions %d", s.stopping, s.pendingStarts, len(s.sessions))
	}
}

func TestServiceShutdownTimesOutButLateStartStillSelfRejects(t *testing.T) {
	root := &blockingRoot{
		path:    t.TempDir(),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	s := New(root)
	s.startDrainTimeout = 10 * time.Millisecond

	type startResult struct {
		id  string
		err error
	}
	startDone := make(chan startResult, 1)
	go func() {
		id, err := s.Start(80, 24)
		startDone <- startResult{id: id, err: err}
	}()
	select {
	case <-root.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not reserve its pending slot before reading the root")
	}

	if err := s.ServiceShutdown(); !errors.Is(err, errStartDrainTimeout) {
		t.Fatalf("ServiceShutdown with blocked Start = %v, want bounded timeout", err)
	}
	select {
	case result := <-startDone:
		t.Fatalf("blocked Start unexpectedly finished before release: %#v", result)
	default:
	}

	close(root.release)
	select {
	case result := <-startDone:
		if result.id != "" || !errors.Is(result.err, errServiceStopping) {
			t.Fatalf("late Start after shutdown timeout = (%q, %v), want stopping error", result.id, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late Start did not self-reject after root call returned")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopping || s.pendingStarts != 0 || s.startsDrained != nil || len(s.sessions) != 0 {
		t.Fatalf(
			"post-timeout state = stopping %v pending %d drained %v sessions %d",
			s.stopping, s.pendingStarts, s.startsDrained != nil, len(s.sessions),
		)
	}
}

func TestServiceShutdownWinsConcurrentStartedProcessAndWaitsForReap(t *testing.T) {
	s := New(fixedRoot(t.TempDir()))
	reachedCommit := make(chan *session, 1)
	releaseCommit := make(chan struct{})
	s.beforeStartCommit = func(sess *session) {
		reachedCommit <- sess
		<-releaseCommit
	}

	type startResult struct {
		id  string
		err error
	}
	startDone := make(chan startResult, 1)
	go func() {
		id, err := s.Start(80, 24)
		startDone <- startResult{id: id, err: err}
	}()

	var started *session
	select {
	case started = <-reachedCommit:
	case result := <-startDone:
		t.Fatalf("terminal failed before deterministic commit gate: id=%q err=%v", result.id, result.err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out starting terminal process")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- s.ServiceShutdown() }()
	waitForTerminalStopping(t, s)
	select {
	case err := <-shutdownDone:
		t.Fatalf("ServiceShutdown returned before pending Start cleaned up: %v", err)
	default:
	}

	close(releaseCommit)
	select {
	case result := <-startDone:
		if result.id != "" || !errors.Is(result.err, errServiceStopping) {
			t.Fatalf("Start that lost shutdown race = (%q, %v), want stopping error", result.id, result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out killing and reaping rejected terminal process")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServiceShutdown did not finish after pending Start cleanup")
	}

	if started.cmd == nil || started.cmd.ProcessState == nil {
		t.Fatal("shutdown-raced terminal process was killed but not reaped with Wait")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingStarts != 0 || len(s.sessions) != 0 {
		t.Fatalf("post-shutdown state = pending %d sessions %d", s.pendingStarts, len(s.sessions))
	}
}

func waitForTerminalStopping(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		stopping := s.stopping
		s.mu.Unlock()
		if stopping {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("ServiceShutdown did not enter stopping state")
		}
		time.Sleep(time.Millisecond)
	}
}
