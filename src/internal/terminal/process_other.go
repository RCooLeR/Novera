//go:build !windows

package terminal

import (
	"errors"
	"syscall"

	pty "github.com/aymanbagabas/go-pty"
)

// go-pty starts Unix commands with Setsid and Setctty. The new session already
// gives the shell its own process group, whose ID is the shell PID. Setpgid is
// incompatible with that session setup and makes the child fail with EPERM.
func prepareTerminalCommand(_ *pty.Cmd) {}

type terminalProcessTree struct {
	pid int
}

func ownTerminalProcessTree(cmd *pty.Cmd) (*terminalProcessTree, error) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil, errors.New("terminal process did not start")
	}
	return &terminalProcessTree{pid: cmd.Process.Pid}, nil
}

func (tree *terminalProcessTree) kill() error {
	if tree == nil || tree.pid <= 0 {
		return nil
	}
	err := syscall.Kill(-tree.pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (tree *terminalProcessTree) close() error { return nil }
