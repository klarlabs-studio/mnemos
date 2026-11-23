//go:build !windows

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// TryLock takes an exclusive, non-blocking lock on f. It reports
// ErrHeld when another holder has it. The kernel drops the lock when the
// holder exits, however it exits, so a crashed worker never wedges capture.
func TryLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // fd fits in int
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrHeld
	}
	return err
}
