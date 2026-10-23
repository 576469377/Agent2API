//go:build !windows

package kiro

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
