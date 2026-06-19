//go:build windows

package agent

import (
	"os/exec"
	"syscall"
)

// hideCmd prevents a console window flashing when run_command executes on Windows.
func hideCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
