// Command lanmesh — headless-клиент mesh-VPN «как Radmin» (без интерфейса).
// Графический вариант — cmd/lanmesh-gui, он только под Windows.
//
// Для запуска нужны права на создание сетевого адаптера:
//
//	Windows (из-под администратора, wintun.dll вшита в бинарник):
//	  lanmesh -network myteam -password hunter2
//	Linux:
//	  sudo ./lanmesh -network myteam -password hunter2
//	  (либо один раз `sudo setcap cap_net_admin+ep ./lanmesh` — и без sudo)
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
	"github.com/sirgochki-source/lanmesh/internal/invite"
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
	port := flag.Int("port", 0,
		"постоянный локальный UDP-порт узла (0 — как раньше, случайный при каждом запуске). "+
			"Задай явно и не меняй между запусками: проброс порта на роутере не будет пересоздаваться, "+
			"а кэш подтверждённых endpoint'ов друзей (см. internal/netcache) не потеряет смысл из-за смены порта")
	iface := flag.String("iface", "lanmesh", "имя виртуального адаптера")
	relay := flag.String("relay", defaults.RelayAddr,
		"ретранслятор для пиров за симметричным NAT; пусто — только прямые соединения (подставь свой)")
	useDHT := flag.Bool("dht", false,
		"экспериментально: искать пиров через публичную DHT сети BitTorrent, не обращаясь ни к одному серверу")
	dhtRelay := flag.Bool("dht-relay", false,
		"вместе с -dht: разрешить сети ретранслятор как запасной путь (иначе непробиваемые пары не соединятся). Должно совпадать у всех участников — режим вшит в ключ сети")
	printTag := flag.Bool("tag", false, "напечатать тег сети (нужен для GET /logs) и выйти")
	sendLogs := flag.Bool("sendlogs", true, "слать диагностику на сигналку (читается по -tag через GET /logs)")
	inviteURL := flag.String("invite", "",
		"ссылка-приглашение вида lanmesh://join?net=…&pass=… — заполняет имя сети, пароль, "+
			"сигналки, ретранслятор и режим обнаружения. Явно заданные флаги важнее ссылки")
	flag.Parse()

	// Ссылка-приглашение заполняет только то, что НЕ задано флагами явно:
	// flag.Visit обходит ровно те флаги, которые пользователь написал сам, — так
	// умолчания (а у -signal и -relay они непустые) не мешают ссылке, а
	// осознанное «хочу другой ретранслятор» не затирается ею.
	if *inviteURL != "" {
		inv, err := invite.Parse(*inviteURL)
		if err != nil {
			log.Fatalf("lanmesh: приглашение не разобрано: %v", err)
		}
		explicit := map[string]bool{}
		flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

		if !explicit["network"] {
			*network = inv.Network
		}
		if !explicit["password"] {
			*password = inv.Password
		}
		if len(inv.Signals) > 0 && !explicit["signal"] {
			*signalURLs = strings.Join(inv.Signals, ",")
		}
		if inv.Relay != nil && !explicit["relay"] {
			*relay = *inv.Relay
		}
		// Режим обнаружения вшит в ключ сети, поэтому ссылка тут главнее флагов:
		// выбрать «свой» режим — это войти в другую сеть и не понять почему.
		switch inv.Discovery {
		case invite.DiscoveryDHT, invite.DiscoveryDHTRelay:
			if (explicit["dht"] && !*useDHT) || (explicit["dht-relay"] && *dhtRelay != (inv.Discovery == invite.DiscoveryDHTRelay)) {
				log.Printf("приглашение задаёт режим %q — флаги -dht/-dht-relay проигнорированы "+
					"(режим вшит в ключ сети, свой выбор увёл бы в другую сеть)", inv.Discovery)
			}
			*useDHT = true
			*dhtRelay = inv.Discovery == invite.DiscoveryDHTRelay
		case invite.DiscoverySignal:
			if explicit["dht"] && *useDHT {
				log.Printf("приглашение задаёт обычные сигналки — флаг -dht проигнорирован " +
					"(режим вшит в ключ сети)")
			}
			*useDHT, *dhtRelay = false, false
		}

		// Что именно взято из ссылки — видно сразу, до подключения. Имя сети и
		// пароль НЕ печатаем: пароль секретен, а имя уходит в лог, который может
		// уехать на сигналку (см. -sendlogs). Серверы же знать полезно: человек
		// вставил ссылку из мессенджера и вправе видеть, куда его ведут.
		applied := []string{}
		if len(inv.Signals) > 0 && !explicit["signal"] {
			applied = append(applied, "сигналки: "+strings.Join(inv.Signals, ", "))
		}
		if inv.Relay != nil && !explicit["relay"] {
			if *inv.Relay == "" {
				applied = append(applied, "ретранслятор: не используется")
			} else {
				applied = append(applied, "ретранслятор: "+*inv.Relay)
			}
		}
		if inv.Discovery != "" {
			applied = append(applied, "обнаружение: "+inv.Discovery)
		}
		if len(applied) > 0 {
			log.Printf("из приглашения — %s", strings.Join(applied, "; "))
		}
	}

	if *network == "" || *password == "" {
		flag.Usage()
		os.Exit(2)
	}

	// Тег — несекретный идентификатор сети на сигналке; по нему забирается
	// диагностика. Считается локально из имени+пароля, в сеть тут ничего не идёт.
	if *printTag {
		mode := ""
		if *useDHT {
			mode = "dht"
			if *dhtRelay {
				mode = "dht+relay"
			}
		}
		fmt.Println(sig.NetworkTag(crypto.DeriveNetworkKeyMode(*network, *password, mode)))
		return
	}

	// Кольцевой буфер лога тройником рядом со stderr: так диагностику headless-узла
	// можно забрать с сигналки по -tag (GET /logs) — раньше это умела только GUI.
	buf := logbuf.New(200)
	log.SetOutput(io.MultiWriter(os.Stderr, buf))

	sess := app.NewSession(splitList(*signalURLs), splitList(*stunServers), *iface)
	sess.EnableLogUpload(buf, *sendLogs)
	sess.UseRelay(*relay)
	// У CLI нет конфига, куда GUI сохраняет выбранный порт (см. cmd/lanmesh-gui) —
	// колбэк сохранения передавать некуда, поэтому nil. -port=0 (по умолчанию)
	// ведёт себя как раньше: PickPort сам выберет случайный порт при каждом
	// запуске и ничего сохранять не попросит.
	sess.SetPort(*port, nil)
	mode := app.DiscoverySignal
	if *useDHT {
		mode = app.DiscoveryDHT
		if *dhtRelay {
			mode = app.DiscoveryDHTRelay
		}
		log.Printf("обнаружение через DHT: сигналки не используются (ретранслятор %s)",
			map[bool]string{true: "разрешён", false: "запрещён"}[*dhtRelay])
	}
	if err := sess.AddNetworkMode(*network, *password, mode); err != nil {
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
