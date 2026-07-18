// Command natcheck — грубая диагностика типа NAT: узнаёт внешний адрес через
// ДВА разных STUN-сервера с ОДНОГО UDP-сокета. Если внешний порт совпал —
// mapping не зависит от назначения (cone NAT), hole punching скорее всего
// сработает. Если порты разные — симметричный NAT, прямой P2P не пробьётся,
// нужен relay.
package main

import (
	"fmt"
	"net"

	"github.com/sirgochki-source/lanmesh/internal/signal"
)

func main() {
	// Окно не должно закрыться раньше, чем прочитают вердикт: при двойном клике по
	// exe консоль исчезает сразу после выхода. defer срабатывает на любом return.
	defer func() {
		fmt.Print("\nНажми Enter, чтобы закрыть окно...")
		fmt.Scanln()
	}()

	// Тот же разнородный список, что и у клиента: заодно видно, какие серверы
	// вообще доступны с этой машины, а какие режет провайдер.
	servers := signal.DefaultSTUNServers

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		fmt.Println("не открыть сокет:", err)
		return
	}
	defer conn.Close()
	fmt.Printf("локальный порт: %d\n", conn.LocalAddr().(*net.UDPAddr).Port)

	var results []string
	for _, s := range servers {
		ep, err := signal.DiscoverEndpoint(conn, s)
		if err != nil {
			fmt.Printf("  %-28s -> ошибка: %v\n", s, err)
			continue
		}
		fmt.Printf("  %-28s -> %s\n", s, ep)
		results = append(results, ep)
	}

	fmt.Println()
	if len(results) == 0 {
		fmt.Println("ВЕРДИКТ: не ответил НИ ОДИН сервер — исходящий UDP режется. Внешний адрес не определить,")
		fmt.Println("         другие участники не смогут до тебя достучаться. Нужен relay.")
		return
	}
	if len(results) < 2 {
		fmt.Println("ВЕРДИКТ: ответил только один сервер — внешний адрес есть, но тип NAT не определить.")
		fmt.Println("         Прямой P2P возможен, но не гарантирован.")
		return
	}
	same := true
	for _, r := range results[1:] {
		if portOf(r) != portOf(results[0]) {
			same = false
		}
	}
	if same {
		fmt.Println("ВЕРДИКТ: внешний порт одинаковый для всех (cone NAT). Hole punching, скорее всего, СРАБОТАЕТ — прямой P2P возможен.")
	} else {
		fmt.Println("ВЕРДИКТ: внешний порт РАЗНЫЙ (симметричный NAT). Прямой P2P НЕ пробьётся — нужен relay (orangepi).")
	}
}

func portOf(hostport string) string {
	_, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return port
}
