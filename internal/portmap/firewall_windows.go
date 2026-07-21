// Входящее правило брандмауэра — условие работоспособности проброса, а не
// удобство. В паре cone↔CGNAT мы слали на рефлексивный адрес IP:портX, а
// входящий приходит с IP:портY (симметричный NAT выдал другой порт роутеру
// снаружи, но роутер честно форвардит его нам на localPort). Для брандмауэра
// Windows это НЕСВЯЗАННЫЙ с исходящим потоком входящий пакет: роутер перешлёт,
// Windows выбросит. Без правила проброс бесполезен ровно в том сценарии, ради
// которого затевался (см. комментарий у pickExternal в internal/app).
//
// Правило привязано к program=, а не открывает порт всем желающим, и снимается
// вместе с маппингом: иначе после отказа от фичи (или её выключения в панели) в
// системе оставалось бы разрешающее правило, о котором пользователь не знает.
package portmap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ruleName — по нему же правило и удаляется, поэтому имя фиксированное.
const ruleName = "lanmesh"

// AllowInbound заводит (или переставляет) входящее правило на localPort — тот
// порт, куда роутер форвардит пакет ПОСЛЕ NAT (не внешний порт из Mapping:
// снаружи он может отличаться, но внутри LAN назначение всегда localPort).
func AllowInbound(localPort int) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("путь к exe: %w", err)
	}
	// Старое правило сносим всегда: порт мог смениться, а netsh при add с тем же
	// именем создаёт ВТОРОЕ правило, а не заменяет первое.
	_ = RemoveInbound()
	return netsh("advfirewall", "firewall", "add", "rule",
		"name="+ruleName, "dir=in", "action=allow", "protocol=UDP",
		fmt.Sprintf("localport=%d", localPort), "program="+exe, "enable=yes")
}

// RemoveInbound снимает правило (best-effort — вызывающий не обязан проверять
// ошибку при штатной уборке на выходе).
func RemoveInbound() error {
	return netsh("advfirewall", "firewall", "delete", "rule", "name="+ruleName)
}

// netsh — та же схема, что в internal/tun/tun_windows.go: таймаут, чтобы
// зависший netsh (битый helper-dll, групповые политики) не вешал вызывающего,
// и HideWindow, чтобы у GUI-сборки не мелькала консоль.
func netsh(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("netsh %v: превышен таймаут (10с)", args)
	}
	if err != nil {
		return fmt.Errorf("netsh %v: %w (%s)", args, err, out)
	}
	return nil
}
