//go:build windows

package mcp

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func configureStdioCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
