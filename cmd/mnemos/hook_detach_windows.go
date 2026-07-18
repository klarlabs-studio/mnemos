//go:build windows

package main

import "syscall"

// Windows process-creation flags. Defined here rather than taken from
// x/sys/windows to keep this a stdlib-only build.
const (
	// detachedProcess starts the child without a console, so it is not tied to
	// the parent's console lifetime.
	detachedProcess = 0x00000008
	// createNewProcessGroup keeps a Ctrl-C in the parent's console from
	// signalling the worker.
	createNewProcessGroup = 0x00000200
)

// detachSysProcAttr is the Windows equivalent of Setsid: the capture worker
// must survive the hook process exiting. syscall.SysProcAttr has no Setsid
// field here — the cross-platform trap that a linux-only CI build misses and
// a release cross-compile catches.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
		HideWindow:    true,
	}
}
