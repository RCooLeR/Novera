//go:build !windows

package agent

import "os/exec"

func hideCmd(_ *exec.Cmd) {}
