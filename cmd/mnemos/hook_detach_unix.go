//go:build !windows

package main

import "syscall"

// detachSysProcAttr puts the capture worker in its own session, so it outlives
// the hook process and is not killed when Claude Code reaps the hook's process
// group.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
