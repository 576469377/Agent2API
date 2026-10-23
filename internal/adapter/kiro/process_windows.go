//go:build windows

package kiro

import (
	"os/exec"
	"syscall"
)

func hideWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
