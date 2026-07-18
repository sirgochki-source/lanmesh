// Package defaults — общие значения серверов по умолчанию для всех клиентов
// (GUI и headless). Держим в одном месте, чтобы при смене боевых адресов не
// пришлось править их в двух cmd/* и не забыть один из них.
//
// ПЛЕЙСХОЛДЕРЫ: сюда боевые адреса НЕ коммитим — их подставляют в настройках
// панели или в config.json (gitignored). Здесь только заглушки для сборки.
package defaults

// SignalURLs — ВСЕ сигналки сразу: клиент объявляется в каждой и сливает списки
// участников (а не переключается между ними). Cloudflare Worker и/или свой сервер
// cmd/lanmesh-signal (25557 под TLS, 25556 плайнтекстом для старых сборок).
var SignalURLs = []string{
	"https://your-worker.example.workers.dev",
	"https://your-server.example.com:25557",
	"http://your-server.example.com:25556",
}

// RelayAddr — ретранслятор (cmd/lanmesh-relay) для пиров за симметричным NAT
// (в частности, за мобильным CGNAT), где прямое пробитие невозможно в принципе.
const RelayAddr = "relay.example.com:25555"
