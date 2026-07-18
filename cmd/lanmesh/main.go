// Command lanmesh — headless-клиент mesh-VPN «как Radmin» (без интерфейса).
// Графический вариант — cmd/lanmesh-gui.
//
// Запуск (из-под администратора, рядом должен лежать wintun.dll):
//
//	lanmesh -network myteam -password hunter2
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sirgochki-source/lanmesh/internal/app"
	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/defaults"
	"github.com/sirgochki-source/lanmesh/internal/logbuf"
	sig "github.com/sirgochki-source/lanmesh/internal/signal"
)

func main() {
	network := flag.String("network", "", "имя сети (общее у всех участников)")
	password := flag.String("password", "", "пароль сети")
	signalURLs := flag.String("signal", strings.Join(defaults.SignalURLs, ","),
		"сигналки через запятую — регистрируемся во всех и сливаем списки участников (подставь свои)")
	stunServers := flag.String("stun", strings.Join(sig.DefaultSTUNServers, ","),
		"STUN-серверы через запятую (опрашиваются разом, берётся первый ответивший)")
	iface := flag.String("iface", "lanmesh", "имя виртуального адаптера")
	relay := flag.String("relay", defaults.RelayAddr,
		"ретранслятор для пиров за симметричным NAT; пусто — только прямые соединения (подставь свой)")
	printTag := flag.Bool("tag", false, "напечатать тег сети (нужен для GET /logs) и выйти")
	sendLogs := flag.Bool("sendlogs", true, "слать диагностику на сигналку (читается по -tag через GET /logs)")
	flag.Parse()

	if *network == "" || *password == "" {
		flag.Usage()
		os.Exit(2)
	}

	// Тег — несекретный идентификатор сети на сигналке; по нему забирается
	// диагностика. Считается локально из имени+пароля, в сеть тут ничего не идёт.
	if *printTag {
		fmt.Println(sig.NetworkTag(crypto.DeriveNetworkKey(*network, *password)))
		return
	}

	// Кольцевой буфер лога тройником рядом со stderr: так диагностику headless-узла
	// можно забрать с сигналки по -tag (GET /logs) — раньше это умела только GUI.
	buf := logbuf.New(200)
	log.SetOutput(io.MultiWriter(os.Stderr, buf))

	sess := app.NewSession(splitList(*signalURLs), splitList(*stunServers), *iface)
	sess.EnableLogUpload(buf, *sendLogs)
	sess.UseRelay(*relay)
	if err := sess.Start(*network, *password); err != nil {
		log.Fatalf("lanmesh: %v", err)
	}
	// Имя сети НЕ логируем — лог уходит на сигналку, а имя ей знать не положено.
	log.Printf("сеть готова — можно играть. Ctrl+C для выхода.")

	// Держим процесс до Ctrl+C, затем чисто снимаем адаптер.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	<-sigc
	sess.Stop()
}

// splitList разбирает список через запятую, отбрасывая пустые элементы и пробелы.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
