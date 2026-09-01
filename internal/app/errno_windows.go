//go:build windows

package app

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isAddrInUse — распознаётся ли ошибка bind'а как «порт занят».
//
// Проверено эмпирически (net.ListenUDP на второй бинд того же порта, см.
// port_test.go/TestListenNodeBusyPortReturnsError): реальная ошибка Windows —
// syscall.Errno(10048), то есть windows.WSAEADDRINUSE. syscall.EADDRINUSE — это
// ВЫМЫШЛЕННАЯ кросс-платформенная константа Go (APPLICATION_ERROR+2), которую
// сетевые вызовы на Windows никогда не возвращают: errors.Is с ней всегда false.
//
// Ради этой единственной константы пакет app тащил бы x/sys/windows целиком и
// не собирался бы под Linux — поэтому проверка живёт здесь, а не в session.go.
func isAddrInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE)
}
