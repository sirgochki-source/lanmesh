// Package winexec — тонкие обёртки над системными утилитами Windows, общие для
// нескольких пакетов. Здесь живёт запуск netsh: и настройка адаптера (internal/tun),
// и правило брандмауэра для проброса (internal/portmap) гоняют его одинаково, а
// дублировать обёртку в двух местах — значит однажды разъехаться в таймауте или
// обработке ошибок.
package winexec

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// Netsh запускает netsh с таймаутом и БЕЗ видимого окна консоли. Таймаут: netsh
// эпизодически виснет (битый helper-dll, групповые политики), а вызовы идут на
// горячих путях (поднятие узла под opMu, заведение правила проброса) — без
// дедлайна завис бы весь старт. HideWindow: GUI-версия без консоли, иначе на
// каждый вызов мигало бы чёрное окно.
func Netsh(args ...string) error {
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
