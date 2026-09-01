# Порт клиента на Linux

Дата: 2026-09-01. Статус: дизайн утверждён, план не написан.

## Контекст

Клиент lanmesh собирается только под Windows. Проверено живой сборкой
(`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`), а не по памяти:
серверная часть (`cmd/lanmesh-relay`, `cmd/lanmesh-signal`) под Linux собирается
уже сегодня, а клиент упирается ровно в четыре места.

| Место | Что именно держит на Windows |
|---|---|
| `internal/tun` | только `tun_windows.go` — Wintun + вшитая `wintun.dll` |
| `internal/app/session.go:684,705` | `windows.WSAEADDRINUSE` тащит `x/sys/windows` в ядро |
| `internal/portmap` | `defaultGateway()` (iphlpapi) и `AllowInbound/RemoveInbound` (netsh) — только `_windows` |
| `internal/winexec` | обёртка над `netsh`, Windows-only по определению |

Всё остальное — `internal/peer`, `crypto`, `signal`, `netcache`, `dhtdisc`,
`proto`, `logbuf`, `defaults`, сам `cmd/lanmesh` — переносимо как есть.

## Область

Входит:

1. `internal/tun/tun_linux.go` — виртуальный адаптер поверх `/dev/net/tun`.
2. `internal/portmap` — определение шлюза и заглушка брандмауэра под Linux.
3. Вынос платформенной проверки «порт занят» из ядра `internal/app`.
4. Единый путь конфига, корректный под `sudo`.
5. `deploy/lanmesh.service` — запуск узла как systemd-сервиса.
6. Релизные ассеты `linux/{amd64,arm64}` для `lanmesh` и `natcheck`, README.

Не входит сознательно:

- **GUI (`cmd/lanmesh-gui`) остаётся Windows-only.** `go-webview2` существует
  только под Windows, `systray` под Linux требует CGO и пакетов
  webkit2gtk/appindicator, а вместе с ними отваливаются кросс-сборка с Windows
  и `CGO_ENABLED=0`. Решение принято явно, а не по умолчанию: цена — весь
  тулчейн сборки, выгода — окно вокруг панели, которая и так работает по
  `http://127.0.0.1:8737`.
- **IPv6 на виртуальном адаптере.** Его нет и на Windows (`assignIP` там
  возвращает ошибку на не-IPv4), виртуальная подсеть — `25.0.0.0/8`. Паритет
  платформ важнее односторонней фичи.
- **Автообновление** — живёт в `cmd/lanmesh-gui/update.go`, то есть в
  Windows-only части.

## Архитектура

Основной принцип: **порт не заводит новых абстракций**. Разделение по платформам
уже существует в проекте (файлы с суффиксом `_windows`), и Linux-ветка встаёт в
ту же схему — новый файл рядом со старым, один и тот же экспортируемый API.
Ни одного `if runtime.GOOS ==` в коде.

Второй принцип: **где платформенное различие вылезло в общий код — оно
убирается, а не дублируется**. Таких мест два: проверка «порт занят» в
`session.go` и вычисление пути конфига. Оба получают по одному хелперу с двумя
реализациями, и все вызывающие — включая Windows-GUI — переходят на него. Два
способа посчитать одно и то же не остаются ни в одном из случаев.

### Карта файлов

Новые:

| Файл | Назначение |
|---|---|
| `internal/tun/tun.go` | общий, без тегов: `virtualMTU = 1280` и док пакета |
| `internal/tun/tun_linux.go` | `Device` поверх `/dev/net/tun` |
| `internal/portmap/gateway_linux.go` | `defaultGateway()` из `/proc/net/route` |
| `internal/portmap/firewall_linux.go` | `AllowInbound`/`RemoveInbound` — no-op + детект фильтра |
| `internal/app/errno_windows.go`, `errno_unix.go` | `isAddrInUse(error) bool` |
| `internal/app/confdir_windows.go`, `confdir_unix.go` | `ConfigDir()` |
| `deploy/lanmesh.service` | systemd-юнит |

Изменяемые:

| Файл | Что |
|---|---|
| `internal/app/session.go` | уходит импорт `x/sys/windows`; 2 проверки ошибки → `isAddrInUse`; 3 вызова `os.UserConfigDir()` → `ConfigDir()` |
| `internal/app/port_test.go` | уходит зависимость от `windows.WSAEADDRINUSE` |
| `cmd/lanmesh-gui/main.go` | 2 вызова `os.UserConfigDir()` → `app.ConfigDir()` |
| `cmd/natcheck/main.go` | «Нажми Enter» — только под Windows |
| `internal/tun/tun_windows.go` | `virtualMTU` переезжает в `tun.go` |
| `cmd/lanmesh/main.go` | шапка: про `wintun.dll` и админа — это про Windows |
| `README.md` | раздел «Linux», обновлённый список релизных ассетов |

`internal/winexec` не трогается: Linux-ветка его не использует, он остаётся
Windows-only обёрткой над `netsh`.

## Компоненты

### `internal/tun/tun_linux.go`

Создание: `unix.Open("/dev/net/tun", O_RDWR|O_CLOEXEC, 0)` →
`ioctl(TUNSETIFF)` с флагами `IFF_TUN|IFF_NO_PI`.

`IFF_NO_PI` обязателен. Без него ядро дописывает к каждому пакету 4 байта
`struct tun_pi`, а `internal/peer` ждёт сырой IP-пакет — ровно то, что отдаёт
Wintun. Молчаливое расхождение здесь выглядело бы как «туннель поднялся, трафик
не ходит».

Имя интерфейса ядро обрезает до `IFNAMSIZ-1` = 15 символов. Фактическое имя
забираем из заполненного ядром `ifreq`; `Name()` возвращает его, а не то, что
просили, — иначе последующая настройка ушла бы в несуществующий интерфейс.

Настройка — `ioctl` на служебном `AF_INET`-сокете:

| ioctl | Значение |
|---|---|
| `SIOCSIFADDR` | виртуальный IP (`25.x.y.z`, из `proto.VirtualIP`) |
| `SIOCSIFNETMASK` | `/8` — как передаёт `tun.New(s.iface, selfIP, 8)` |
| `SIOCSIFMTU` | `virtualMTU` = 1280 |
| `SIOCSIFFLAGS` | добавить `IFF_UP` и `IFF_RUNNING` |

Связанный маршрут на `25.0.0.0/8` ядро заводит само при поднятии интерфейса с
адресом — отдельного `RTM_NEWROUTE` не нужно.

Рассмотренные альтернативы настройки:

- **rtnetlink** (`RTM_NEWADDR`, `RTM_SETLINK`) — современный API и единственный
  путь к IPv6-адресу на адаптере и собственным маршрутам. Отклонён: ~200 строк
  ручной сборки атрибутов и разбора `NLMSG_ERROR` ради возможности, которой в
  проекте нет ни на одной платформе.
- **exec `ip`** (iproute2) — прямая калька с `winexec.Netsh`, самый короткий
  код. Отклонён: внешняя зависимость на iproute2 ради основной функциональности,
  ошибки текстом в stderr вместо кодов, и `exec` на горячем пути поднятия узла —
  ровно то, из-за чего в `winexec` пришлось заводить таймаут.

Выбран `ioctl`: ~60 строк на `golang.org/x/sys/unix` (модуль `x/sys` уже в
зависимостях), ноль внешних утилит, `CGO_ENABLED=0` сохраняется, кросс-сборка с
Windows работает. Единственное, чего он не умеет, — IPv6, и он не нужен.

**Read/Close.** Дескриптор переводится в неблокирующий режим
(`unix.SetNonblock`) и оборачивается в `os.NewFile`: рантайм Go берёт его в
epoll, `Read` блокируется в netpoller, а `Close` штатно будит читателя и отдаёт
`os.ErrClosed`. Это та же схема, что в `wireguard-go`.

Linux-версия при этом получается проще Windows-овой, и это не случайность:
Wintun падает с access violation, если закрыть сессию под живым читателем,
поэтому там понадобились `sessMu`, `closed atomic.Bool` и пробуждение через
`SetEvent`. На Linux ничего этого не нужно — внешний контракт (`Read`, `Write`,
`Name`, `Close`, `os.ErrClosed` после закрытия) остаётся тем же.

Интерфейс непостоянный: исчезает при закрытии дескриптора, отдельная уборка не
нужна и `Close` не обязан ничего доделывать за ядром.

Ошибки открытия разделяются, потому что лечатся по-разному:

- файла нет → «модуль tun не загружен, попробуй `modprobe tun`»;
- `EPERM`/`EACCES` → «нужен `sudo` или `setcap cap_net_admin+ep`».

### `internal/portmap/gateway_linux.go`

`defaultGateway()` читает `/proc/net/route` и берёт строку с
`Destination == 00000000` и флагами `RTF_UP|RTF_GATEWAY` (`0x1|0x2`); при
нескольких маршрутах по умолчанию — с наименьшей метрикой.

Порядок байт проверен на живой системе (WSL2, ядро 6.6): поле `Gateway` со
значением `0190A8C0` соответствует `192.168.144.1`, что совпало с выводом
`ip route`. Раскладываем теми же байтами, что и Windows-версия: младший байт
32-битного значения — первый октет. Это верно на little-endian, то есть на обеих
целевых архитектурах (amd64, arm64 LE); ограничение фиксируется комментарием.

`rtnetlink RTM_GETROUTE` отклонён по той же причине, что и в `tun`: длиннее без
выигрыша. IPv6-шлюз каскаду PCP/NAT-PMP не нужен — они работают по IPv4.

Отсутствие файла или отсутствие маршрута по умолчанию — не паника, а ошибка
того же вида, что и на Windows: каскад в `portmap.go` уже умеет работать без
шлюза, оставляя только UPnP (он находит роутер сам через SSDP).

**Здесь уместен юнит-тест** — это чистый парсер с неочевидными краями
(несколько дефолтных маршрутов с разными метриками, строки с `RTF_GATEWAY` без
`RTF_UP`, мусор, пустой файл). Пишется после того, как код заработает, и
проверяется мутационной пробой.

### `internal/portmap/firewall_linux.go`

Правил не меняет. На Windows входящее правило — условие работоспособности
проброса: строгий stateful-фильтр отбрасывает входящий пакет, пришедший с
адреса, отличного от того, куда мы слали. На Linux без активного фильтра
входящий UDP на забинденный порт проходит, и отдельного разрешения не требуется.

Менять чужой `ufw`/`firewalld` из приложения — вторжение, которое переживёт
процесс: пользователь не ожидает, что VPN-клиент правил его firewall, а снятое
«best-effort» правило может и не сняться. Но и молчать, когда проброс есть, а
входящих нет, нельзя — диагностика в этом случае врёт.

Поэтому `AllowInbound` возвращает `nil`, но один раз за цикл проброса пытается
определить активный фильтр: `ufw status` / `firewall-cmd --state` с таймаутом
2 секунды, только если бинарь есть в `PATH`, результат кэшируется на время
жизни процесса, любая ошибка — молчание. Если фильтр активен, узел пишет в лог
и в диагностику точную подсказку.

Это `exec` — тот самый, против которого выше отклонён вариант с `ip`.
Различие принципиальное: там `exec` был на горячем пути поднятия узла и нёс
основную функциональность, здесь он вызывается один раз, вне горячего пути, и
его отказ не влияет ни на что, кроме текста подсказки.

`RemoveInbound` возвращает `nil`: снимать нечего.

### `internal/app` — `isAddrInUse`

`session.go` дважды сравнивает ошибку bind'а с `windows.WSAEADDRINUSE`, и
только из-за этого тащит `golang.org/x/sys/windows` в ядро сессии.

Заменяется на `isAddrInUse(err error) bool`:

- `errno_windows.go` — `errors.Is(err, windows.WSAEADDRINUSE)`;
- `errno_unix.go` (`//go:build !windows`) — `errors.Is(err, syscall.EADDRINUSE)`.

Существующий комментарий о том, что `syscall.EADDRINUSE` — вымышленная
кросс-платформенная константа Go (`APPLICATION_ERROR+2`), которую сетевые вызовы
Windows никогда не возвращают, переезжает в `errno_windows.go`: он объясняет
именно Windows-реализацию и в общем коде теперь не к месту.

`port_test.go` сегодня пиннит платформенную форму ошибки — конкретную константу
Windows. Это ровно тот случай, когда тест зафиксировал случайную форму, а не
инвариант: переписывается на проверку «повторный bind занятого порта
распознаётся как занятый» через `isAddrInUse` и становится
кросс-платформенным. Факт переписывания отмечается в отчёте.

### `internal/app` — `ConfigDir`

Сейчас путь считается через `os.UserConfigDir()` в пяти местах: `netcachePath`,
`dhtNodesPath`, `LoadOrCreateIdentity` и дважды в GUI.

Под `sudo` это ломается по-настоящему, а не косметически. Виртуальный IP узла
выводится из `PeerID`, который лежит в `identity` рядом с конфигом. Если путь
уезжает в `/root/.config`, узел получает **другую идентичность и другой адрес в
сети** в зависимости от того, как его запустили, а кэш подтверждённых
endpoint'ов теряет смысл.

Вводится `app.ConfigDir() (string, error)`:

- `confdir_windows.go` — `os.UserConfigDir()`, поведение Windows не меняется;
- `confdir_unix.go` — если `euid == 0` и задан `SUDO_UID`, берём домашнюю папку
  этого пользователя (`os/user.LookupId`, читает `/etc/passwd`, работает без
  CGO) и `.config`; иначе `os.UserConfigDir()`.

Плюс `chown` создаваемого каталога и файлов на `SUDO_UID:SUDO_GID`. Без этого
`identity` остаётся root-owned с правами `0700` в домашней папке пользователя, и
запуск чего угодно без `sudo` — того же `natcheck` — упирается в отказ доступа.

На этот хелпер переходят все пять вызовов, включая Windows-GUI: два способа
посчитать один путь не остаются.

Под systemd `SUDO_*` нет, и `os.UserConfigDir()` отработает по
`XDG_CONFIG_HOME`, который задаёт юнит (см. ниже) — отдельной ветки «мы сервис»
в коде не появляется.

### `deploy/lanmesh.service`

```ini
[Unit]
Description=lanmesh
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/lanmesh/lanmesh.env
Environment=XDG_CONFIG_HOME=/var/lib
StateDirectory=lanmesh
ExecStart=/usr/local/bin/lanmesh $LANMESH_ARGS
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

`StateDirectory=lanmesh` создаёт `/var/lib/lanmesh`, а `XDG_CONFIG_HOME=/var/lib`
заставляет `os.UserConfigDir()` вернуть именно его — без `/root/.config` и без
кода, знающего про systemd. Параметры сети идут через `EnvironmentFile` с
правами `0600`: пароль сети не место в юните, который читается всеми.

## Сборка и релиз

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o lanmesh-linux-amd64  ./cmd/lanmesh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o natcheck-linux-amd64 ./cmd/natcheck
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o lanmesh-linux-arm64  ./cmd/lanmesh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o natcheck-linux-arm64 ./cmd/natcheck
```

Релиз становится 3 + 4 = 7 ассетов. Windows-часть релиз-процесса не меняется.

`cmd/natcheck` заканчивается «Нажми Enter, чтобы закрыть окно» — это спасает от
схлопывания консоли при двойном клике по `exe` и не имеет смысла в
Linux-терминале. Ожидание остаётся только под Windows.

README получает раздел «Linux»: сборка, запуск под `sudo`,
`setcap cap_net_admin+ep` как альтернатива, systemd-юнит и явное «GUI под Linux
нет».

## Проверка

Проверка живым прогоном — основная; сборка и тесты её не заменяют.

1. Кросс-сборка обеих целевых архитектур + `GOOS=linux go vet ./...`.
2. `go test ./...` нативно в WSL2 (Ubuntu, ядро 6.6, `/dev/net/tun` на месте).
3. `sudo ./lanmesh -network … -password …`; проверить `ip addr show lanmesh`:
   адрес `25.x.y.z/8`, `mtu 1280`, флаг `UP`; проверить связанный маршрут
   `25.0.0.0/8` в `ip route`.
4. **Реальный трафик между Windows-хостом и WSL2 в одной сети**: два узла,
   ping виртуального IP в обе стороны, затем TCP через туннель. Это и есть
   доказательство того, что порт работает, а не просто собирается.
5. Под `sudo` файлы легли в `~/.config/lanmesh` и принадлежат пользователю, а
   не root; запуск `natcheck` без `sudo` читает ту же идентичность.
6. Мутационная проба для юнит-теста парсера `/proc/net/route`.

Чего эта проверка не покрывает — фиксируется явно, а не замалчивается: WSL2
сидит за собственным NAT Hyper-V, поэтому пара Windows↔WSL сойдётся по
LAN-кандидату. Пробитие через STUN и проброс порта на роутере в этой связке
по-настоящему не проверяются; `natcheck` под Linux запускается, но его вердикт
будет про NAT самой WSL. Для полной проверки нужна отдельная Linux-машина или
VPS; до тех пор эти два пункта остаются непроверенными и помечены как таковые.
