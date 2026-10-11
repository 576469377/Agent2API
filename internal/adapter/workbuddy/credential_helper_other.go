//go:build !windows

package workbuddy

import "os/exec"

func hideCredentialHelper(cmd *exec.Cmd) {}
