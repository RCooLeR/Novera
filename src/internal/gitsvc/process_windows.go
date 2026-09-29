//go:build windows

package gitsvc

import (
	"os/exec"
	"syscall"
)

// hideWindow prevents a console window flashing when git runs on Windows.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
