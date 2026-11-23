//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockFile takes an exclusive, non-blocking lock on f. It reports
// errLockHeld when another holder has it. Windows releases the lock when the
// holder's handle closes, including when the process dies.
func tryLockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockHeld
	}
	return err
}
