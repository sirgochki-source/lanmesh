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
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sirgochki-source/lanmesh/internal/app"
	"github.com/sirgochki-source/lanmesh/internal/crypto"
	sig "github.com/sirgochki-source/lanmesh/internal/signal"
)

func main() {
	network := flag.String("network", "", "имя сети (общее у всех участников)")
	password := flag.String("password", "", "пароль сети")
	signalURLs := flag.String("signal",
		"https://your-worker.example.workers.dev,https://your-server.example.com:25557,http://your-server.example.com:25556",
		"сигналки через запятую — регистрируемся во всех и сливаем списки участников (подставь свои)")
	stunServers := flag.String("stun", strings.Join(sig.DefaultSTUNServers, ","),
		"STUN-серверы через запятую (опрашиваются разом, берётся первый ответивший)")
	iface := flag.String("iface", "lanmesh", "имя виртуального адаптера")
	relay := flag.String("relay", "relay.example.com:25555",
		"ретранслятор для пиров за симметричным NAT; пусто — только прямые соединения (подставь свой)")
	printTag := flag.Bool("tag", false, "напечатать тег сети (нужен для GET /logs) и выйти")
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

	sess := app.NewSession(splitList(*signalURLs), splitList(*stunServers), *iface)
	sess.UseRelay(*relay)
	if err := sess.Start(*network, *password); err != nil {
		log.Fatalf("lanmesh: %v", err)
	}
	log.Printf("сеть %q готова — можно играть. Ctrl+C для выхода.", *network)

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
