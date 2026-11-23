// Package filelock takes exclusive, non-blocking advisory locks on files:
// flock on Unix, LockFileEx on Windows. The operating system releases a lock
// when its holder's file is closed or its process exits, however it exits, so a
// crashed holder never leaves a lock behind.
package filelock

import "errors"

// ErrHeld reports that another holder has the lock.
var ErrHeld = errors.New("lock held")
