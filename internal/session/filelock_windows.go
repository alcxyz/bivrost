//go:build windows

package session

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	errorLockViolation      = syscall.Errno(33)
	errorIOPending          = syscall.Errno(997)
)

// Windows locks are mandatory, so lock one byte far beyond any record: other
// processes can still read the file while its owner holds the lock.
func lockRegion() *syscall.Overlapped {
	return &syscall.Overlapped{OffsetHigh: 0x7fffffff}
}

// tryLockFile takes an exclusive lock on f without waiting. The operating
// system releases it when the file is closed or the process dies.
func tryLockFile(f *os.File) (bool, error) {
	region := lockRegion()
	r, _, err := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(region)))
	if r != 0 {
		return true, nil
	}
	if errors.Is(err, errorLockViolation) || errors.Is(err, errorIOPending) {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) error {
	region := lockRegion()
	r, _, err := procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(region)))
	if r == 0 {
		return err
	}
	return nil
}
