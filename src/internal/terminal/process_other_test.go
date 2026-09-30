//go:build !windows

package terminal

import (
	"syscall"
	"testing"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

func TestTerminalOwnsIsolatedProcessGroup(t *testing.T) {
	p, err := pty.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	cmd := p.Command("/bin/sh", "-c", "exec sleep 30")
	prepareTerminalCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start terminal with a controlling PTY: %v", err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("terminal was not reaped during cleanup")
		}
	})

	group, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if group != cmd.Process.Pid || group == syscall.Getpgrp() {
		t.Fatalf("terminal process group = %d, want PID %d isolated from parent group %d", group, cmd.Process.Pid, syscall.Getpgrp())
	}
	tree, err := ownTerminalProcessTree(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.kill(); err != nil {
		t.Fatalf("kill owned process group: %v", err)
	}
	select {
	case <-done:
		if waitErr == nil || cmd.ProcessState == nil {
			t.Fatalf("terminal was not reaped after group kill: state=%v err=%v", cmd.ProcessState, waitErr)
		}
		status := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatalf("terminal exit status = %v, want SIGKILL", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal process group was not terminated")
	}
}
