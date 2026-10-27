//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

func configureStdioCommand(cmd *exec.Cmd) {
	// A new process group lets Close signal the server and anything it spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type processTree struct {
	pgid int
}

func attachProcessTree(cmd *exec.Cmd) *processTree {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return &processTree{pgid: cmd.Process.Pid}
}

func (p *processTree) kill(cmd *exec.Cmd) {
	if p != nil && p.pgid > 0 {
		_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (p *processTree) release() {}
