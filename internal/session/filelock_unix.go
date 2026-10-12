//go:build !windows

package session

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes an exclusive advisory lock on f without waiting. The
// operating system releases it when the file is closed or the process dies.
func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
