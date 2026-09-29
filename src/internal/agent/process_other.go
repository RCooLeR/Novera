//go:build !windows

package agent

import (
	"errors"
	"os/exec"
	"syscall"
)

func hideCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type commandTree struct {
	pid int
}

func ownCommandTree(cmd *exec.Cmd) (*commandTree, error) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil, errors.New("command process did not start")
	}
	return &commandTree{pid: cmd.Process.Pid}, nil
}

func (tree *commandTree) kill() error {
	if tree == nil || tree.pid <= 0 {
		return nil
	}
	err := syscall.Kill(-tree.pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// close terminates any background descendants that survived their shell's
// natural exit. The shell owns a fresh process group, so this cannot target the
// Novera process group.
func (tree *commandTree) close() error {
	return tree.kill()
}
