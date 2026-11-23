package filelock_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.klarlabs.de/mnemos/internal/filelock"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A second holder is refused while the first holds the lock, and admitted once
// the first closes its file.
func TestTryLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, b := open(t, path), open(t, path)
	defer func() { _ = b.Close() }()
	if err := filelock.TryLock(a); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := filelock.TryLock(b); !errors.Is(err, filelock.ErrHeld) {
		t.Fatalf("second lock while held: err = %v, want ErrHeld", err)
	}
	_ = a.Close()
	if err := filelock.TryLock(b); err != nil {
		t.Fatalf("lock after the holder closed: %v", err)
	}
}
