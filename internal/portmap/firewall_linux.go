package portmap

// На Windows входящее правило брандмауэра — условие работоспособности проброса
// (см. firewall_windows.go): встроенный фильтр отбрасывает входящий пакет,
// пришедший НЕ с того адреса, куда мы слали, и без правила проброс бесполезен
// ровно в том сценарии, ради которого затевался. На Linux аналога нет: без
// активного фильтра входящий UDP на забинденный порт проходит, разрешать нечего.
//
// Править чужой ufw/firewalld мы сознательно не будем: это вторжение, которое
// переживёт процесс — пользователь не ждёт, что VPN-клиент менял его firewall,
// а «best-effort» снятие правила может и не сработать. Но и молчать нельзя:
// при включённом фильтре проброс есть, входящих нет, и диагностика врёт.
// Поэтому правил не трогаем, активный фильтр обнаруживаем и говорим прямо.

import (
	"context"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// AllowInbound на Linux ничего не разрешает — см. комментарий к файлу. Сигнатура
// сохранена, чтобы вызывающий код в internal/app был общим для платформ.
func AllowInbound(int) error {
	warnIfFiltered()
	return nil
}

// RemoveInbound на Linux ничего не снимает: правил мы не заводили.
func RemoveInbound() error { return nil }

var warnOnce sync.Once

// warnIfFiltered один раз за жизнь процесса проверяет, включён ли ufw или
// firewalld, и если да — пишет подсказку в лог (он же уходит в диагностику,
// см. Session.EnableLogUpload).
//
// Да, это exec — тот самый, из-за которого отклонён вариант с утилитой `ip` в
// internal/tun. Разница принципиальная: там exec был на горячем пути поднятия
// узла и нёс основную функциональность, здесь он вызывается один раз, вне
// горячего пути, и его отказ не влияет ни на что, кроме текста подсказки.
func warnIfFiltered() {
	warnOnce.Do(func() {
		if name, ok := activeFilter(); ok {
			log.Printf("portmap: активен %s — входящие на проброшенный порт могут "+
				"отбрасываться; если пиры не соединяются напрямую, разреши UDP-порт узла", name)
		}
	})
}

// activeFilter — best-effort определение включённого фильтра. Любая осечка
// (бинаря нет, нет прав, таймаут, незнакомый вывод) читается как «фильтра нет»:
// ложная тревога хуже молчания, потому что уводит диагностику в сторону.
func activeFilter() (string, bool) {
	probes := []struct{ bin, arg, want string }{
		{"ufw", "status", "status: active"},
		{"firewall-cmd", "--state", "running"},
	}
	for _, p := range probes {
		path, err := exec.LookPath(p.bin)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, path, p.arg).CombinedOutput()
		cancel()
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(string(out)), p.want) {
			return p.bin, true
		}
	}
	return "", false
}
