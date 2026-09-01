//go:build !windows

package app

import (
	"errors"
	"syscall"
)

// isAddrInUse — распознаётся ли ошибка bind'а как «порт занят». На Unix, в
// отличие от Windows (см. errno_windows.go), syscall.EADDRINUSE — настоящий
// errno из ядра, и errors.Is по нему работает как задумано.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
