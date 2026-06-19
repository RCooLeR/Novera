//go:build !windows

package gitsvc

import "os/exec"

func hideWindow(_ *exec.Cmd) {}
