package workbuddy

import (
	"os/exec"
	"syscall"
)

func hideCredentialHelper(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
