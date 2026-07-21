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
	"fmt"
	"os"

	"github.com/sirgochki-source/lanmesh/internal/winexec"
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
	return winexec.Netsh("advfirewall", "firewall", "add", "rule",
		"name="+ruleName, "dir=in", "action=allow", "protocol=UDP",
		fmt.Sprintf("localport=%d", localPort), "program="+exe, "enable=yes")
}

// RemoveInbound снимает правило (best-effort — вызывающий не обязан проверять
// ошибку при штатной уборке на выходе).
func RemoveInbound() error {
	return winexec.Netsh("advfirewall", "firewall", "delete", "rule", "name="+ruleName)
}
