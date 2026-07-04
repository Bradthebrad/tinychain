//go:build !windows

package mcp

import "os/exec"

func configureStdioCommand(cmd *exec.Cmd) {}
