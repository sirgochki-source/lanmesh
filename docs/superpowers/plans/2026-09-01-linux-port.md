# Порт клиента на Linux — план реализации

> **Для агентов:** выполнять по задачам через superpowers:subagent-driven-development
> или superpowers:executing-plans. Шаги отмечаются чекбоксами (`- [ ]`).

**Goal:** собрать и доказать живым прогоном headless-клиент lanmesh (`cmd/lanmesh`
и `cmd/natcheck`) под Linux amd64/arm64, не меняя поведения на Windows.

**Архитектура:** порт не вводит новых абстракций — Linux-реализации встают в уже
существующую схему файлов с суффиксом платформы (`_windows` → рядом `_linux`),
экспортируемый API пакетов не меняется. Два места, где платформенное протекло в
общий код (проверка «порт занят» и вычисление пути конфига), сводятся к одному
хелперу с двумя реализациями, на который переходят ВСЕ вызывающие, включая
Windows-GUI: параллельных путей не остаётся.

**Tech Stack:** Go 1.26, `golang.org/x/sys/unix` (уже прямая зависимость),
`/dev/net/tun` + `ioctl`, `/proc/net/route`, systemd. `CGO_ENABLED=0`.

**Spec:** `docs/superpowers/specs/2026-09-01-linux-port-design.md`

## Global Constraints

- `CGO_ENABLED=0` для всех Linux-сборок; ни одной новой зависимости в `go.mod`.
- Целевые платформы: `linux/amd64`, `linux/arm64`. Windows-поведение не меняется
  ни в одном файле — это требование, а не пожелание.
- Ни одного `if runtime.GOOS ==`: разделение только build-тегами и суффиксами.
- Комментарии и сообщения об ошибках — по-русски, как во всём проекте.
- **Тесты пишутся ПОСЛЕ реализации и только там, где реально нужны** (инвариант,
  который глазами не проверить; хитрый парсер с краями). Шагов «прогнать тест,
  убедиться что падает» в плане нет намеренно.
- Один коммит на задачу.
- Go на dev-машине: `C:\Users\ivest\go-sdk\go\bin\go.exe` (в PATH не прописан).

---

### Task 1: `isAddrInUse` — убрать `x/sys/windows` из ядра сессии

`internal/app/session.go` дважды сравнивает ошибку bind'а с
`windows.WSAEADDRINUSE`, и только из-за этого весь пакет не собирается под
Linux.

**Files:**
- Create: `internal/app/errno_windows.go`, `internal/app/errno_unix.go`
- Modify: `internal/app/session.go` (импорты; строки 684 и 705)
- Modify: `internal/app/port_test.go` (импорты; `TestListenNodeBusyPortReturnsError`)

**Interfaces:**
- Produces: `func isAddrInUse(err error) bool` — непубличная, пакет `app`.

- [ ] **Шаг 1: создать `internal/app/errno_windows.go`**

```go
//go:build windows

package app

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isAddrInUse — распознаётся ли ошибка bind'а как «порт занят».
//
// Проверено эмпирически (net.ListenUDP на второй бинд того же порта, см.
// port_test.go/TestListenNodeBusyPortReturnsError): реальная ошибка Windows —
// syscall.Errno(10048), то есть windows.WSAEADDRINUSE. syscall.EADDRINUSE — это
// ВЫМЫШЛЕННАЯ кросс-платформенная константа Go (APPLICATION_ERROR+2), которую
// сетевые вызовы на Windows никогда не возвращают: errors.Is с ней всегда false.
// Ради этой единственной константы пакет app тащил бы x/sys/windows целиком, —
// поэтому проверка живёт здесь, а не в session.go.
func isAddrInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE)
}
```

- [ ] **Шаг 2: создать `internal/app/errno_unix.go`**

```go
//go:build !windows

package app

import (
	"errors"
	"syscall"
)

// isAddrInUse — распознаётся ли ошибка bind'а как «порт занят». На Unix, в
// отличие от Windows (см. errno_windows.go), syscall.EADDRINUSE — настоящий
// errno из ядра, и errors.Is по нему работает как задумано.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
```

- [ ] **Шаг 3: переключить `session.go` на хелпер**

В `listenNode` (около строки 684) заменить

```go
	if errors.Is(err, windows.WSAEADDRINUSE) {
```
на
```go
	if isAddrInUse(err) {
```

В `bringUpNode` (около строки 705) заменить

```go
	if err != nil && errors.Is(err, windows.WSAEADDRINUSE) {
```
на
```go
	if err != nil && isAddrInUse(err) {
```

Из комментария над `listenNode` убрать абзац про `windows.WSAEADDRINUSE` и
вымышленную константу (он переехал в `errno_windows.go`), оставив объяснение,
ЗАЧЕМ занятый порт возвращается ошибкой, а не деградирует в udp4. Удалить из
блока импортов `"golang.org/x/sys/windows"`. Проверить, что `"errors"` ещё
используется в файле — он используется, удалять его не нужно.

- [ ] **Шаг 4: переписать `TestListenNodeBusyPortReturnsError` на инвариант**

Тест сегодня пиннит платформенную форму ошибки (константу Windows), а не
инвариант. Убрать импорт `"golang.org/x/sys/windows"` и заменить тело проверки
и комментарий:

```go
// Занятый порт обязан вернуть ОШИБКУ, а не тихую деградацию в udp4: иначе
// listenNode истолковал бы случайную коллизию порта (теперь, с PickPort, порт
// не всегда 0 — конфликт стал возможен) как «нет IPv6-стека» и молча оставил бы
// узел без IPv6 на весь сеанс.
//
// Проверяем инвариант, а не форму ошибки: конкретный код различается по
// платформам (WSAEADDRINUSE против EADDRINUSE), и распознаёт его isAddrInUse.
func TestListenNodeBusyPortReturnsError(t *testing.T) {
	busy, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatalf("сокет: %v", err)
	}
	defer busy.Close()
	port := busy.LocalAddr().(*net.UDPAddr).Port

	conn, err := listenNode(port)
	if err == nil {
		conn.Close()
		t.Fatalf("занятый порт %d обязан вернуть ошибку, получили рабочий сокет", port)
	}
	if !isAddrInUse(err) {
		t.Fatalf("ошибка занятого порта не распознана isAddrInUse: %v", err)
	}
}
```

Убрать из импортов `port_test.go` и `"golang.org/x/sys/windows"`, и `"errors"`:
проверено — `errors.` в этом файле встречалось ровно один раз, в переписанной
строке 78, так что после правки оба импорта становятся неиспользуемыми и файл
без их удаления не соберётся.

- [ ] **Шаг 5: проверить, что на Windows ничего не сломалось**

```
go build ./... && go test ./internal/app/ -run TestListenNode -v
```
Ожидается: сборка проходит, оба `TestListenNode*` — PASS. Это и есть
доказательство отсутствия регрессии: тест на занятый порт продолжает ловить ту
же ситуацию, просто через хелпер.

- [ ] **Шаг 6: мутационная проба переписанного теста**

Временно в `errno_windows.go` заменить тело на `return false`, прогнать
`go test ./internal/app/ -run TestListenNodeBusyPortReturnsError`. Ожидается
FAIL. Вернуть тело обратно, прогнать снова — PASS. Без этой пробы переписанный
тест ничего не доказывает.

- [ ] **Шаг 7: коммит**

```bash
git add internal/app/errno_windows.go internal/app/errno_unix.go internal/app/session.go internal/app/port_test.go
git commit -m "refactor(app): развести проверку занятого порта по платформам"
```

---

### Task 2: `ConfigDir` — единый путь конфига, корректный под sudo

**Files:**
- Create: `internal/app/confdir_windows.go`, `internal/app/confdir_unix.go`
- Modify: `internal/app/session.go` (`netcachePath` ~305, `dhtNodesPath` ~554,
  `LoadOrCreateIdentity` ~1881)
- Modify: `cmd/lanmesh-gui/main.go` (два вызова `os.UserConfigDir()`, ~1054 и ~1096)

**Interfaces:**
- Produces: `func ConfigDir() (string, error)` — ЭКСПОРТИРУЕМАЯ (её зовёт
  `cmd/lanmesh-gui`); `func chownToSudoUser(path string)` — непубличная.

- [ ] **Шаг 1: создать `internal/app/confdir_windows.go`**

```go
//go:build windows

package app

import "os"

// ConfigDir — каталог, в котором лежат identity, config.json и кэши
// (endpoints.json, dht-nodes.dat). Единственный источник этого пути: и сессия,
// и GUI зовут его, чтобы не было двух способов посчитать одно и то же.
//
// На Windows это просто %AppData%: аналога sudo, подменяющего домашнюю папку
// процесса, здесь нет — GUI перезапускается через UAC под тем же пользователем.
func ConfigDir() (string, error) { return os.UserConfigDir() }

// chownToSudoUser на Windows не делает ничего: владельца менять не нужно и
// некому. Существует, чтобы вызывающий код был общим для обеих платформ.
func chownToSudoUser(string) {}
```

- [ ] **Шаг 2: создать `internal/app/confdir_unix.go`**

```go
//go:build !windows

package app

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// ConfigDir — каталог, в котором лежат identity, config.json и кэши.
//
// Создание сетевого адаптера требует root, то есть обычный запуск — `sudo
// lanmesh`. При этом os.UserConfigDir() указал бы на /root/.config: HOME под
// sudo зависит от настроек sudoers, а XDG_CONFIG_HOME обычно вычищается
// env_reset. Для lanmesh это не косметика — виртуальный IP узла выводится из
// PeerID (proto.VirtualIP), который лежит в identity рядом с конфигом. Уехал
// путь — узел получил ДРУГУЮ идентичность и другой адрес в сети, а кэш
// подтверждённых endpoint'ов (internal/netcache) потерял смысл.
//
// Поэтому под root'ом, поднятым через sudo, берём домашнюю папку исходного
// пользователя. Под systemd переменных SUDO_* нет, и путь считается обычным
// образом: юнит задаёт XDG_CONFIG_HOME=/var/lib, и os.UserConfigDir() сам
// отдаёт /var/lib — отдельной ветки «мы сервис» в коде не появляется.
func ConfigDir() (string, error) {
	if u, ok := sudoUser(); ok {
		return filepath.Join(u.HomeDir, ".config"), nil
	}
	return os.UserConfigDir()
}

// sudoUser возвращает исходного пользователя, если процесс поднят через sudo.
// user.LookupId без CGO читает /etc/passwd — этого достаточно и сборку это не
// усложняет.
func sudoUser() (*user.User, bool) {
	if os.Geteuid() != 0 {
		return nil, false
	}
	uid := os.Getenv("SUDO_UID")
	if uid == "" {
		return nil, false
	}
	u, err := user.LookupId(uid)
	if err != nil || u.HomeDir == "" {
		return nil, false
	}
	return u, true
}

// chownToSudoUser передаёт созданный путь исходному пользователю. Без этого
// identity остаётся root-owned с правами 0600 в каталоге 0700 внутри домашней
// папки пользователя, и любой последующий запуск БЕЗ sudo (тот же natcheck)
// упирается в отказ доступа. Best-effort: не смогли — не повод не работать.
func chownToSudoUser(path string) {
	u, ok := sudoUser()
	if !ok {
		return
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return
	}
	_ = os.Chown(path, uid, gid)
}
```

- [ ] **Шаг 3: перевести три места в `session.go` на `ConfigDir`**

В `netcachePath` и `dhtNodesPath` заменить `os.UserConfigDir()` на `ConfigDir()`
(тела в остальном не меняются).

`LoadOrCreateIdentity` заменить целиком:

```go
// LoadOrCreateIdentity читает PeerID из конфига или создаёт новый при первом запуске.
func LoadOrCreateIdentity() (proto.PeerID, error) {
	dir, err := ConfigDir()
	if err != nil {
		return proto.PeerID{}, err
	}
	path := filepath.Join(dir, "lanmesh", "identity")

	if data, err := os.ReadFile(path); err == nil {
		return proto.ParsePeerID(string(data))
	}

	id, err := proto.NewPeerID()
	if err != nil {
		return id, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return id, err
	}
	// Каталог и файл создаёт root (адаптер иначе не поднять) — вернуть их
	// пользователю обязательно, иначе запуск без sudo больше не прочитает
	// идентичность и узел сменит адрес в сети. На Windows — no-op.
	chownToSudoUser(filepath.Dir(path))
	if err := os.WriteFile(path, []byte(id.String()), 0600); err != nil {
		return id, err
	}
	chownToSudoUser(path)
	return id, nil
}
```

- [ ] **Шаг 4: перевести GUI на тот же хелпер**

В `cmd/lanmesh-gui/main.go` в обеих функциях, где стоит `os.UserConfigDir()`
(~1054 и ~1096), заменить на `app.ConfigDir()`. Пакет `app` там уже
импортирован. Проверить, что `os` в файле ещё используется — используется.

Смысл шага: не оставить второй способ считать путь. Поведение Windows при этом
не меняется — `confdir_windows.go` возвращает ровно `os.UserConfigDir()`.

- [ ] **Шаг 5: проверка на Windows**

```
go build ./... && go test ./internal/app/ ./cmd/lanmesh-gui/
```
Ожидается: сборка и тесты проходят. Дополнительно вручную: запустить
`lanmesh-gui.exe`, убедиться, что он видит существующий `config.json` в
`%AppData%\lanmesh` (то есть путь не поехал).

- [ ] **Шаг 6: коммит**

```bash
git add internal/app/confdir_windows.go internal/app/confdir_unix.go internal/app/session.go cmd/lanmesh-gui/main.go
git commit -m "refactor(app): единый ConfigDir, корректный под sudo"
```

---

### Task 3: `internal/tun` — виртуальный адаптер на Linux

**Files:**
- Create: `internal/tun/tun.go` (общий), `internal/tun/tun_linux.go`
- Modify: `internal/tun/tun_windows.go` (убрать `virtualMTU`, сократить док пакета)

**Interfaces:**
- Consumes: ничего из прошлых задач.
- Produces: `func New(name string, ip netip.Addr, prefixBits int) (*Device, error)`,
  методы `(*Device) Read([]byte) (int, error)`, `Write([]byte) (int, error)`,
  `Name() string`, `Close() error` — сигнатуры те же, что у Windows-версии.
  Внутрипакетная константа `virtualMTU = 1280`.

- [ ] **Шаг 1: подготовить Linux-окружение для проверки**

Go в WSL не установлен. Поставить (версия должна совпадать с dev-машиной, 1.26.5):

```bash
wsl.exe -d Ubuntu -- bash -lc 'cd /tmp && curl -fsSLO https://go.dev/dl/go1.26.5.linux-amd64.tar.gz && sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.26.5.linux-amd64.tar.gz && /usr/local/go/bin/go version'
```
Ожидается: `go version go1.26.5 linux/amd64`. `sudo` в WSL спросит пароль —
команду выполняет пользователь (в сессии это `! <команда>`).

Проверить предпосылки (уже подтверждено при проектировании, повторить на всякий):
```bash
wsl.exe -d Ubuntu -- bash -lc 'ls -l /dev/net/tun && ip route | head -3'
```
Ожидается: устройство существует, есть маршрут `default via ...`.

- [ ] **Шаг 2: создать `internal/tun/tun.go` — общая часть**

```go
// Package tun оборачивает виртуальный сетевой адаптер: создание, назначение
// виртуального IP и чтение/запись IP-пакетов.
//
// Реализации платформенные — Wintun на Windows (tun_windows.go), /dev/net/tun
// на Linux (tun_linux.go), — но контракт Device общий:
//
//   - Read/Write работают с СЫРЫМ IP-пакетом, без каких-либо служебных
//     заголовков перед ним;
//   - Close безопасен при активном Read, и после него Read возвращает ошибку,
//     удовлетворяющую errors.Is(err, os.ErrClosed).
//
// Обе реализации требуют повышенных прав: администратор на Windows, root или
// CAP_NET_ADMIN на Linux — создание сетевого адаптера иначе недоступно.
package tun

// virtualMTU — MTU виртуального адаптера. Туннель добавляет к каждому пакету
// служебные байты, и худший случай — путь ЧЕРЕЗ РЕТРАНСЛЯТОР:
//   IP(20)+UDP(8)+нонс(12)+тег(16)+заголовок кадра(17)+relay-заголовок(49) = 122.
// Прежние 1400 считались без relay-заголовка (49Б): напрямую пакет 1400 давал
// 1473Б на проводе и влезал в 1500, а через relay — 1522Б, то есть за пределом
// Ethernet. Крупные пакеты (чанки Minecraft) при этом дробились/терялись, и через
// ретранслятор соединение вроде «в сети», а по факту виснет.
// Берём 1280 — минимум IPv6 и стандарт WireGuard/Tailscale: с запасом переживает
// и relay, и мобильный LTE/CGNAT (там путь уже 1500 и фрагменты часто режут).
//
// Значение общее для всех платформ сознательно: разъехавшийся MTU дал бы
// односторонние потери только на крупных пакетах — дефект, который почти
// невозможно связать с причиной.
const virtualMTU = 1280
```

- [ ] **Шаг 3: почистить `tun_windows.go`**

Удалить из него объявление `const virtualMTU = 1280` вместе с комментарием
(переехали в `tun.go`) и док-комментарий пакета (`// Package tun ...` со всеми
строками до `package tun`), оставив вместо него обычный комментарий о том, что
это Wintun-реализация:

```go
//go:build windows

// Wintun-реализация Device. Требует wintun.dll (вшита, см. ensureWintun) и
// запуска с правами администратора.
package tun
```

Остальное содержимое файла не трогать.

- [ ] **Шаг 4: создать `internal/tun/tun_linux.go`**

```go
//go:build linux

// Реализация Device поверх /dev/net/tun. Требует root или CAP_NET_ADMIN.
package tun

import (
	"errors"
	"fmt"
	"net/netip"
	"os"

	"golang.org/x/sys/unix"
)

// cloneDevice — управляющее устройство, через которое ядро выдаёт новый tun.
const cloneDevice = "/dev/net/tun"

// Device — открытый tun-интерфейс.
//
// В отличие от Wintun-версии здесь не нужны ни мьютекс, ни собственный флаг
// закрытия. Дескриптор переведён в неблокирующий режим и обёрнут в os.File,
// поэтому рантайм Go держит его в epoll: Read висит в netpoller, а Close
// штатно будит читателя, и тот получает os.ErrClosed. С Wintun так нельзя —
// закрытие сессии под живым читателем роняет процесс, отсюда sessMu, closed и
// SetEvent в tun_windows.go.
type Device struct {
	f    *os.File
	name string
}

// New создаёт интерфейс name, назначает ему ip в подсети /prefixBits, ставит
// MTU и поднимает интерфейс.
func New(name string, ip netip.Addr, prefixBits int) (*Device, error) {
	// Только IPv4: маска считается как IPv4 и виртуальная подсеть — 25.0.0.0/8
	// (proto.VirtualPrefix). Ровно то же ограничение, что у Windows-версии.
	if !ip.Is4() {
		return nil, fmt.Errorf("tun: ожидался IPv4-адрес, получен %s", ip)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return nil, fmt.Errorf("tun: имя %q не годится (максимум %d символов): %w",
			name, unix.IFNAMSIZ-1, err)
	}

	fd, err := unix.Open(cloneDevice, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, openError(err)
	}

	// IFF_NO_PI обязателен: без него ядро дописывает к каждому пакету 4 байта
	// struct tun_pi, а internal/peer ждёт сырой IP-пакет — ровно то, что отдаёт
	// Wintun. Разошлись бы молча: адаптер «поднят», трафик не ходит.
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("tun: TUNSETIFF %q (нужен root или CAP_NET_ADMIN): %w", name, err)
	}
	// Дальше настраиваем по имени, которое вернуло ЯДРО, а не по запрошенному:
	// иначе ioctl'ы ушли бы в несуществующий интерфейс.
	actual := ifr.Name()

	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("tun: SetNonblock: %w", err)
	}
	d := &Device{f: os.NewFile(uintptr(fd), cloneDevice), name: actual}

	if err := configure(actual, ip, prefixBits); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// configure выставляет адрес, маску и MTU и поднимает интерфейс — всё ioctl'ами
// на служебном AF_INET-сокете.
//
// Рассматривались rtnetlink и вызов утилиты `ip`. rtnetlink отклонён как ~200
// строк ручной сборки атрибутов ради IPv6-адреса и своих маршрутов, которых в
// проекте нет ни на одной платформе; `ip` — как внешняя зависимость на iproute2
// на горячем пути поднятия узла, с ошибками текстом вместо кодов.
func configure(name string, ip netip.Addr, prefixBits int) error {
	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("tun: служебный сокет: %w", err)
	}
	defer unix.Close(sock)

	set := func(req uint, what string, fill func(*unix.Ifreq) error) error {
		ifr, err := unix.NewIfreq(name)
		if err != nil {
			return err
		}
		if err := fill(ifr); err != nil {
			return fmt.Errorf("tun: %s для %s: %w", what, name, err)
		}
		if err := unix.IoctlIfreq(sock, req, ifr); err != nil {
			return fmt.Errorf("tun: не удалось задать %s на %s: %w", what, name, err)
		}
		return nil
	}

	a4 := ip.As4()
	if err := set(unix.SIOCSIFADDR, "адрес", func(ifr *unix.Ifreq) error {
		return ifr.SetInet4Addr(a4[:])
	}); err != nil {
		return err
	}
	mask := maskBytes(prefixBits)
	if err := set(unix.SIOCSIFNETMASK, "маску", func(ifr *unix.Ifreq) error {
		return ifr.SetInet4Addr(mask[:])
	}); err != nil {
		return err
	}
	if err := set(unix.SIOCSIFMTU, "MTU", func(ifr *unix.Ifreq) error {
		ifr.SetUint32(virtualMTU)
		return nil
	}); err != nil {
		return err
	}

	// Флаги читаем и ДОПОЛНЯЕМ, а не перезаписываем: ядро уже выставило свои
	// (IFF_POINTOPOINT/IFF_NOARP у tun), и затирать их нельзя.
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(sock, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("tun: чтение флагов %s: %w", name, err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(sock, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("tun: не удалось поднять %s: %w", name, err)
	}
	// Связанный маршрут на подсеть (25.0.0.0/8) ядро заводит само вместе с
	// адресом на поднятом интерфейсе — отдельный RTM_NEWROUTE не нужен.
	return nil
}

// Read блокируется до прихода IP-пакета из ОС и копирует его в buf.
func (d *Device) Read(buf []byte) (int, error) { return d.f.Read(buf) }

// Write отправляет IP-пакет в ОС (как будто он пришёл из сети).
func (d *Device) Write(pkt []byte) (int, error) { return d.f.Write(pkt) }

// Name возвращает фактическое имя интерфейса — то, которое выдало ядро.
func (d *Device) Name() string { return d.name }

// Close закрывает дескриптор. Интерфейс непостоянный и исчезает вместе с ним,
// доубирать за ядром нечего. Безопасен при активном Read: рантайм разбудит
// читателя, и тот получит ошибку, оборачивающую os.ErrClosed.
func (d *Device) Close() error { return d.f.Close() }

// maskBytes переводит длину префикса в 4 байта маски (для /8 — 255.0.0.0).
func maskBytes(bits int) [4]byte {
	var m [4]byte
	for i := 0; i < 4 && bits > 0; i++ {
		if bits >= 8 {
			m[i] = 0xff
			bits -= 8
			continue
		}
		m[i] = byte(0xff << (8 - bits))
		bits = 0
	}
	return m
}

// openError переводит отказ открытия /dev/net/tun в понятную причину: нет
// модуля tun и нет прав лечатся по-разному, а сырой errno об этом не говорит.
func openError(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return fmt.Errorf("tun: нет %s — модуль tun не загружен, попробуй `sudo modprobe tun`: %w",
			cloneDevice, err)
	case errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
		return fmt.Errorf("tun: нет прав на %s — запусти через sudo или выдай бинарнику "+
			"`sudo setcap cap_net_admin+ep`: %w", cloneDevice, err)
	}
	return fmt.Errorf("tun: открытие %s: %w", cloneDevice, err)
}
```

- [ ] **Шаг 5: проверить компиляцию под обе архитектуры**

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./internal/tun/
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./internal/tun/
GOOS=linux go vet ./internal/tun/
go build ./internal/tun/           # Windows-ветка не сломана
```

- [ ] **Шаг 6: живая проверка адаптера в WSL2**

Написать во временный каталог (НЕ в репозиторий) пробник `probe.go`:

```go
package main

import (
	"log"
	"net/netip"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/tun"
)

func main() {
	d, err := tun.New("lanmesh", netip.MustParseAddr("25.1.2.3"), 8)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("поднят интерфейс %s", d.Name())
	go func() {
		time.Sleep(10 * time.Second)
		log.Printf("закрываем")
		d.Close()
	}()
	buf := make([]byte, 2048)
	for {
		n, err := d.Read(buf)
		if err != nil {
			log.Printf("Read вернул: %v", err) // ожидаем ошибку с os.ErrClosed
			return
		}
		log.Printf("пакет %d байт", n)
	}
}
```

Прогнать в WSL: `sudo /usr/local/go/bin/go run ./probe.go`, параллельно в другом
окне `ip addr show lanmesh` и `ip route | grep 25\.`.

Ожидается: адрес `25.1.2.3/8`, `mtu 1280`, флаг `UP`; в `ip route` — строка
`25.0.0.0/8 dev lanmesh`; после `ping -c1 25.9.9.9` в логе пробника появляется
«пакет N байт»; через 10 секунд Read возвращает ошибку и процесс выходит, а не
виснет — это и есть проверка контракта Close при активном Read.

Пробник после проверки удалить.

- [ ] **Шаг 7: коммит**

```bash
git add internal/tun/tun.go internal/tun/tun_linux.go internal/tun/tun_windows.go
git commit -m "feat(tun): реализация адаптера на /dev/net/tun для Linux"
```

---

### Task 4: `internal/portmap` — шлюз и брандмауэр на Linux

**Files:**
- Create: `internal/portmap/gateway_linux.go`, `internal/portmap/firewall_linux.go`
- Create: `internal/portmap/gateway_linux_test.go`

**Interfaces:**
- Produces: `func defaultGateway() (netip.Addr, error)` (зовётся из
  `portmap.go:117`), `func AllowInbound(localPort int) error`,
  `func RemoveInbound() error` (зовутся из `internal/app/session.go:962,979`).
  Сигнатуры совпадают с Windows-версиями. Плюс тестируемая
  `func parseDefaultGateway(r io.Reader) (netip.Addr, error)`.

- [ ] **Шаг 1: создать `internal/portmap/gateway_linux.go`**

```go
package portmap

// Определение IP шлюза по умолчанию — единственный системный вызов пакета.
// Вынесен в отдельный _linux-файл по тому же принципу, что и gateway_windows.go:
// вся платформо-зависимость собрана в файлах с суффиксом платформы, а ядро
// каскада (portmap.go) и разбор протоколов (pcp/natpmp/upnp) остаются
// переносимыми.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// procRoute — таблица маршрутов ядра в текстовом виде. rtnetlink (RTM_GETROUTE)
// дал бы то же самое втрое длиннее: IPv6-шлюз каскаду не нужен (PCP, NAT-PMP и
// UPnP говорят с роутером по IPv4), а больше ничего netlink здесь не добавляет.
const procRoute = "/proc/net/route"

// Флаги маршрута из linux/route.h.
const (
	rtfUp      = 0x1
	rtfGateway = 0x2
)

func defaultGateway() (netip.Addr, error) {
	f, err := os.Open(procRoute)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("portmap: %s: %w", procRoute, err)
	}
	defer f.Close()
	return parseDefaultGateway(f)
}

// parseDefaultGateway выбирает шлюз маршрута по умолчанию из содержимого
// /proc/net/route. Формат — заголовок и строки, разделённые табуляцией:
//
//	Iface  Destination  Gateway   Flags  RefCnt  Use  Metric  Mask      MTU Window IRTT
//	eth0   00000000     0190A8C0  0003   0       0    0       00000000  0   0      0
//
// Маршрут по умолчанию — тот, у которого Destination == 0 и подняты флаги
// RTF_UP|RTF_GATEWAY. Их может быть несколько (две сетевые карты, поднятый VPN)
// — берём с наименьшей метрикой, как выбирает и само ядро.
//
// Порядок байт: ядро печатает %08X от значения В СЕТЕВОМ ПОРЯДКЕ, поэтому на
// little-endian младший байт числа — первый октет адреса. Проверено на живой
// системе: 0190A8C0 == 192.168.144.1, совпало с выводом `ip route`. Это та же
// конвенция, что у DWORD-полей в gateway_windows.go, и она НЕ совпадает с
// BigEndian из пакетов PCP/NAT-PMP/UPnP — две конвенции разные, путать нельзя.
// Целевые архитектуры (amd64, arm64) little-endian; на big-endian разбор был бы
// зеркальным.
func parseDefaultGateway(r io.Reader) (netip.Addr, error) {
	var best netip.Addr
	bestMetric := 0
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 7 {
			continue // заголовок или мусор
		}
		dest, e1 := strconv.ParseUint(f[1], 16, 32)
		gw, e2 := strconv.ParseUint(f[2], 16, 32)
		flags, e3 := strconv.ParseUint(f[3], 16, 32)
		metric, e4 := strconv.Atoi(f[6])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue
		}
		if dest != 0 || gw == 0 || flags&(rtfUp|rtfGateway) != rtfUp|rtfGateway {
			continue
		}
		if best.IsValid() && metric >= bestMetric {
			continue
		}
		best = netip.AddrFrom4([4]byte{byte(gw), byte(gw >> 8), byte(gw >> 16), byte(gw >> 24)})
		bestMetric = metric
	}
	if err := sc.Err(); err != nil {
		return netip.Addr{}, fmt.Errorf("portmap: чтение %s: %w", procRoute, err)
	}
	if !best.IsValid() {
		return netip.Addr{}, errors.New("portmap: маршрута по умолчанию нет (узел не за роутером)")
	}
	return best, nil
}
```

- [ ] **Шаг 2: создать `internal/portmap/firewall_linux.go`**

```go
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
```

- [ ] **Шаг 3: проверить компиляцию и живой разбор шлюза**

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./internal/portmap/
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./internal/portmap/
GOOS=linux go vet ./internal/portmap/
go build ./internal/portmap/      # Windows-ветка цела
```

Живая проверка в WSL: временный пробник, печатающий `defaultGateway()`, и рядом
`ip route | head -1`. Ожидается совпадение адресов (на текущей WSL —
`192.168.144.1`). Это главная проверка задачи; тест ниже её не заменяет.

- [ ] **Шаг 4: написать тест парсера — ПОСЛЕ того, как живой прогон сошёлся**

Парсер — чистая функция с неочевидными краями (несколько дефолтов с разными
метриками, `RTF_GATEWAY` без `RTF_UP`, шлюз 0.0.0.0, мусор), и глазами эти края
не проверить. Создать `internal/portmap/gateway_linux_test.go`:

```go
package portmap

import (
	"strings"
	"testing"
)

const routeHeader = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"

// Обычный случай: один маршрут по умолчанию. Заодно фиксирует порядок байт —
// 0190A8C0 обязан читаться как 192.168.144.1 (проверено на живой системе против
// `ip route`); перепутанный порядок дал бы 1.144.168.192 и увёл бы PCP/NAT-PMP
// стучаться не туда.
func TestParseDefaultGatewaySingle(t *testing.T) {
	in := routeHeader +
		"eth0\t00000000\t0190A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"eth0\t0090A8C0\t00000000\t0001\t0\t0\t0\t00F0FFFF\t0\t0\t0\n"
	gw, err := parseDefaultGateway(strings.NewReader(in))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if gw.String() != "192.168.144.1" {
		t.Fatalf("шлюз разобран как %s, ожидали 192.168.144.1", gw)
	}
}

// Несколько маршрутов по умолчанию (две сетевые карты или поднятый VPN): берём
// с наименьшей метрикой, как выбирает ядро. Порядок строк не должен влиять.
func TestParseDefaultGatewayLowestMetric(t *testing.T) {
	in := routeHeader +
		"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"eth0\t00000000\t0190A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n"
	gw, err := parseDefaultGateway(strings.NewReader(in))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if gw.String() != "192.168.144.1" {
		t.Fatalf("выбран %s, ожидали шлюз с меньшей метрикой 192.168.144.1", gw)
	}
}

// Края, каждый из которых обязан быть отброшен: маршрут без RTF_UP, маршрут без
// RTF_GATEWAY, нулевой шлюз, битая строка. Ни один не должен стать ответом.
func TestParseDefaultGatewaySkipsUnusable(t *testing.T) {
	cases := map[string]string{
		"без RTF_UP":      "eth0\t00000000\t0190A8C0\t0002\t0\t0\t0\t00000000\t0\t0\t0\n",
		"без RTF_GATEWAY": "eth0\t00000000\t0190A8C0\t0001\t0\t0\t0\t00000000\t0\t0\t0\n",
		"нулевой шлюз":    "eth0\t00000000\t00000000\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
		"мусор":           "это не таблица маршрутов вовсе\n",
		"пусто":           "",
	}
	for name, body := range cases {
		if gw, err := parseDefaultGateway(strings.NewReader(routeHeader + body)); err == nil {
			t.Fatalf("%s: ожидали ошибку, получили шлюз %s", name, gw)
		}
	}
}
```

- [ ] **Шаг 5: прогнать тесты и сделать мутационную пробу**

```
GOOS=linux go test ./internal/portmap/ -run TestParseDefaultGateway -v
```
(либо нативно в WSL — файл собирается только под Linux).

Мутационная проба, без неё теста нет: временно поменять порядок байт в
`netip.AddrFrom4` на обратный — `TestParseDefaultGatewaySingle` обязан покраснеть;
временно убрать проверку `flags&(rtfUp|rtfGateway)` — обязан покраснеть
`TestParseDefaultGatewaySkipsUnusable`. Вернуть код, прогнать снова — зелено.

- [ ] **Шаг 6: коммит**

```bash
git add internal/portmap/gateway_linux.go internal/portmap/firewall_linux.go internal/portmap/gateway_linux_test.go
git commit -m "feat(portmap): шлюз из /proc/net/route и детект фильтра на Linux"
```

---

### Task 5: собрать бинарники целиком

После задач 1–4 у пакетов есть Linux-реализации; здесь добиваются команды
верхнего уровня и проверяется полная кросс-сборка.

**Files:**
- Create: `cmd/natcheck/pause_windows.go`, `cmd/natcheck/pause_other.go`
- Modify: `cmd/natcheck/main.go` (блок `defer` в начале `main`)
- Modify: `cmd/lanmesh/main.go` (шапка пакета)

**Interfaces:**
- Produces: `func pause()` в пакете `main` команды `natcheck`.

- [ ] **Шаг 1: вынести паузу natcheck в платформенные файлы**

`cmd/natcheck/pause_windows.go`:

```go
//go:build windows

package main

import "fmt"

// pause держит окно открытым: при двойном клике по exe консоль исчезает сразу
// после выхода процесса, и вердикт прочитать не успевают.
func pause() {
	fmt.Print("\nНажми Enter, чтобы закрыть окно...")
	fmt.Scanln()
}
```

`cmd/natcheck/pause_other.go`:

```go
//go:build !windows

package main

// pause на Linux не нужна: natcheck запускают из терминала, который никуда не
// девается, а лишнее ожидание ввода ломало бы запуск из скрипта.
func pause() {}
```

В `cmd/natcheck/main.go` заменить блок в начале `main`:

```go
	// Окно не должно закрыться раньше, чем прочитают вердикт (см. pause).
	defer pause()
```

и убрать из импортов `"fmt"`, ЕСЛИ он больше нигде в файле не используется —
он используется дальше, поэтому импорт остаётся.

- [ ] **Шаг 2: поправить шапку `cmd/lanmesh/main.go`**

Заменить строки о запуске так, чтобы они не утверждали Windows-специфику как
общую:

```go
// Command lanmesh — headless-клиент mesh-VPN «как Radmin» (без интерфейса).
// Графический вариант — cmd/lanmesh-gui, он только под Windows.
//
// Запуск с правами на создание сетевого адаптера:
//
//	Windows (администратор, wintun.dll вшита в exe):
//	  lanmesh -network myteam -password hunter2
//	Linux:
//	  sudo ./lanmesh -network myteam -password hunter2
//	  (или один раз `sudo setcap cap_net_admin+ep ./lanmesh` и запускать без sudo)
```

- [ ] **Шаг 3: полная кросс-сборка**

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...
GOOS=linux go vet ./...
go build ./... && go vet ./...
```
Ожидается: все четыре команды без ошибок. Первые две — главный признак того,
что порт состоялся на уровне сборки: под Linux собирается ВЕСЬ модуль, включая
`cmd/lanmesh` и `cmd/natcheck` (GUI и smokerun исключены build-тегами).

- [ ] **Шаг 4: прогнать тесты обеих платформ**

На Windows: `go test ./...`.
В WSL: `/usr/local/go/bin/go test ./...` — важно, потому что часть тестов
(`internal/peer`, `internal/app`) впервые исполняется под Linux, а не только
компилируется. Разобраться с каждым падением: если сломан инвариант — чинить
код; если тест пиннил Windows-специфику — переписать и отметить это в отчёте.

- [ ] **Шаг 5: коммит**

```bash
git add cmd/natcheck/pause_windows.go cmd/natcheck/pause_other.go cmd/natcheck/main.go cmd/lanmesh/main.go
git commit -m "feat(cmd): собрать lanmesh и natcheck под Linux"
```

---

### Task 6: живой прогон — доказать, что туннель работает

Это ключевая задача плана: сборка и тесты не доказывают, что порт работает.

**Files:** изменений в репозитории нет; результат — зафиксированные наблюдения.

- [ ] **Шаг 1: собрать Linux-бинарь и запустить узел в WSL2**

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/lanmesh ./cmd/lanmesh
```
(либо собрать в самой WSL). Запуск:
```bash
sudo /tmp/lanmesh -network porttest -password <пароль> -port 34567
```

- [ ] **Шаг 2: проверить состояние адаптера**

```bash
ip addr show lanmesh
ip route | grep '^25\.'
```
Ожидается: `inet 25.x.y.z/8`, `mtu 1280`, флаг `UP`; маршрут `25.0.0.0/8 dev lanmesh`.
Записать фактический виртуальный IP — он нужен на следующем шаге.

- [ ] **Шаг 3: поднять второй узел на Windows-хосте в той же сети**

```
.\lanmesh.exe -network porttest -password <тот же пароль> -port 34568
```
Дождаться в логах обеих сторон появления пира.

- [ ] **Шаг 4: прогнать реальный трафик в обе стороны**

С Windows: `ping <виртуальный IP узла в WSL>`.
Из WSL: `ping -c4 <виртуальный IP узла на Windows>`.
Затем TCP через туннель: на одной стороне слушатель
(`python3 -m http.server 8080 --bind <свой виртуальный IP>`), с другой —
`curl http://<виртуальный IP>:8080/`.

Ожидается: ping отвечает в обе стороны, HTTP-ответ приходит. Это и есть
доказательство порта — сырые IP-пакеты ходят через `/dev/net/tun` в обе стороны
и корректно шифруются/расшифровываются на стыке с Windows-узлом.

- [ ] **Шаг 5: проверить путь конфига и владельца файлов**

```bash
ls -la ~/.config/lanmesh/
```
Ожидается: каталог существует (НЕ `/root/.config/lanmesh`), `identity`,
`endpoints.json` принадлежат обычному пользователю, а не root.

Затем запустить без sudo и убедиться, что идентичность читается та же:
```bash
/tmp/lanmesh -network porttest -password <пароль> -tag
```
(`-tag` не поднимает адаптер, прав не требует; отдельно убедиться, что
`natcheck` без sudo не спотыкается о права на конфиг).

- [ ] **Шаг 6: снять адаптер и убедиться в чистой уборке**

Ctrl+C на узле в WSL, затем `ip addr show lanmesh`. Ожидается: интерфейса нет —
он непостоянный и исчез вместе с дескриптором.

- [ ] **Шаг 7: зафиксировать непроверенное**

Записать в отчёт явно: WSL2 сидит за собственным NAT Hyper-V, поэтому пара
Windows↔WSL сошлась по LAN-кандидату; **пробитие через STUN и проброс порта на
роутере этой проверкой не покрыты**. Запустить `natcheck` под Linux и приложить
вывод, отметив, что его вердикт описывает NAT самой WSL, а не реального роутера.
Коммита в этой задаче нет — она про доказательство, а не про код.

---

### Task 7: systemd, README и релизные сборки

**Files:**
- Create: `deploy/lanmesh.service`
- Modify: `README.md`

- [ ] **Шаг 1: создать `deploy/lanmesh.service`**

```ini
[Unit]
Description=lanmesh — виртуальная локалка
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# Параметры сети (включая пароль) — в отдельном файле с правами 0600:
#   LANMESH_ARGS=-network myteam -password hunter2 -port 34567
EnvironmentFile=/etc/lanmesh/lanmesh.env
# StateDirectory создаёт /var/lib/lanmesh, а XDG_CONFIG_HOME заставляет
# os.UserConfigDir() вернуть /var/lib — так identity и кэши ложатся туда, а не
# в /root/.config, и коду не нужно знать, что он запущен сервисом.
Environment=XDG_CONFIG_HOME=/var/lib
StateDirectory=lanmesh
ExecStart=/usr/local/bin/lanmesh $LANMESH_ARGS
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

- [ ] **Шаг 2: проверить юнит на живой системе**

В WSL systemd может быть выключен — проверить `systemctl is-system-running`.
Если systemd активен: положить бинарь в `/usr/local/bin/lanmesh`, создать
`/etc/lanmesh/lanmesh.env` (0600), `systemctl daemon-reload`,
`systemctl start lanmesh`, затем `systemctl status lanmesh`,
`ip addr show lanmesh` и `ls -la /var/lib/lanmesh/lanmesh/`.
Ожидается: сервис `active (running)`, адаптер поднят, конфиг в `/var/lib/lanmesh/lanmesh/`.

Если systemd в WSL недоступен — **не выдавать юнит за проверенный**: отметить в
отчёте как непроверенный и указать, что для проверки нужна обычная Linux-машина.

- [ ] **Шаг 3: дополнить README**

Добавить раздел «Linux» рядом с существующим «Сборка»:

````markdown
## Linux

Работает headless-клиент (`lanmesh`) и диагностика (`natcheck`). GUI под Linux
нет: он построен на WebView2 и системном трее Windows.

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o lanmesh ./cmd/lanmesh
sudo ./lanmesh -network myteam -password hunter2 -port 34567
```

Создание адаптера требует прав. Либо `sudo`, либо один раз выдать возможность
самому бинарнику и запускать от обычного пользователя:

```
sudo setcap cap_net_admin+ep ./lanmesh
```

Если `/dev/net/tun` нет — не загружен модуль: `sudo modprobe tun`.

Конфиг (identity, кэш endpoint'ов и узлов DHT) лежит в `~/.config/lanmesh`.
Под `sudo` берётся домашняя папка исходного пользователя, а не `/root`, — иначе
узел получал бы разный виртуальный IP в зависимости от способа запуска.

Правило брандмауэра, в отличие от Windows, не заводится: без активного фильтра
входящий UDP проходит. Если включён `ufw`/`firewalld`, узел напишет об этом в
лог — порт нужно разрешить самому.

Фоновый запуск на сервере — `deploy/lanmesh.service`.
````

Также обновить упоминание релизных ассетов, если оно есть в README.

- [ ] **Шаг 4: собрать релизные ассеты**

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o lanmesh-linux-amd64  ./cmd/lanmesh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o natcheck-linux-amd64 ./cmd/natcheck
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o lanmesh-linux-arm64  ./cmd/lanmesh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "-s -w" -o natcheck-linux-arm64 ./cmd/natcheck
```
Проверить, что amd64-бинарь запускается в WSL (`./lanmesh-linux-amd64 -h`), а
arm64 хотя бы опознаётся как ELF нужной архитектуры (`file lanmesh-linux-arm64`).
Сами файлы в репозиторий не коммитить — `.gitignore` проверить на этот счёт.

- [ ] **Шаг 5: коммит**

```bash
git add deploy/lanmesh.service README.md docs/superpowers/specs/2026-09-01-linux-port-design.md docs/superpowers/plans/2026-09-01-linux-port.md
git commit -m "docs(linux): systemd-юнит, раздел README и дизайн порта"
```

---

## Итоговая проверка перед сдачей

- [ ] `go build ./... && go vet ./... && go test ./...` на Windows — зелено,
      поведение GUI не изменилось (панель видит старый `config.json`).
- [ ] `GOOS=linux GOARCH=amd64` и `GOARCH=arm64` — сборка всего модуля.
- [ ] `go test ./...` нативно в WSL — зелено.
- [ ] Туннель Windows↔WSL прогоняет ping и TCP в обе стороны (Task 6, шаг 4).
- [ ] Конфиг под `sudo` лежит у пользователя и принадлежит ему (Task 6, шаг 5).
- [ ] В отчёте явно перечислено непроверенное: пробитие через STUN, проброс
      порта на роутере и (если systemd в WSL недоступен) сам юнит.
