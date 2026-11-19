// Package filelock holds a lock that den's processes share, such as the
// daemon and a den command it runs alongside.
package filelock

import (
	"fmt"
	"os"
	"syscall"
)

// Lock waits for the lock at path, creating the file when it is missing,
// and holds it until the returned function runs.
func Lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}
