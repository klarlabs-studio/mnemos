//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes an exclusive, non-blocking lock on f. It reports
// errLockHeld when another holder has it. The kernel drops the lock when the
// holder exits, however it exits, so a crashed worker never wedges capture.
func tryLockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // fd fits in int
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLockHeld
	}
	return err
}
