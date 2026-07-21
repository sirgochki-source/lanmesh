// Package peer — движок mesh-сети: шифрованный UDP-транспорт между пирами,
// маршрутизация IP-пакетов, пробитие NAT и эмуляция широковещания для LAN-игр.
package peer

import (
	"encoding/binary"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
	"github.com/sirgochki-source/lanmesh/internal/signal"
)

// TUN — минимальный интерфейс виртуального адаптера (чтобы движок не зависел от
// платформенного пакета tun напрямую).
type TUN interface {
	Read(buf []byte) (int, error)
	Write(pkt []byte) (int, error)
}

// Раскладка расшифрованного кадра: [0]=тип, [1:17]=PeerID отправителя,
// [17:25]=счётчик (anti-replay, big-endian), [25:]=нагрузка. Счётчик внутри AEAD —
// подделать его без ключа нельзя, а окно приёма (peerState) режет повторы.
const (
	frameCounterOff = 1 + 16
	frameHeader     = 1 + 16 + 8
	maxPacket       = 1500 // MTU виртуальной сети
	maxFrame        = frameHeader + maxPacket
)

// Интервалы обслуживания.
const (
	punchInterval = 2 * time.Second  // как часто долбим неподтверждённые endpoint'ы
	pingInterval  = 5 * time.Second  // ping подтверждённому пиру: и замер RTT, и keepalive
	peerTimeout   = 60 * time.Second // без пакетов дольше — считаем endpoint протухшим
	rttStale      = 30 * time.Second // RTT старше — врать не будем, показываем «неизвестно»
	// staleProbe — подтверждённый пир молчит дольше (≈3 пропущенных ping): путь под
	// подозрением. Не ждём peerTimeout (иначе смена адреса = минута отвала, а без
	// ретранслятора его нечем прикрыть) — возобновляем пробитие кандидатов и
	// ускоряем регистрацию, чтобы быстрее переоткрыться через сигналку.
	staleProbe = 15 * time.Second

	// peerForget — сколько пир должен отсутствовать в ответах сигналки, прежде
	// чем мы его забудем. Заведомо больше пары циклов регистрации (20с), чтобы
	// одна осечка сигналки не сносила пробитое соединение.
	peerForget = 90 * time.Second

	// relayGrace — сколько даём на честное пробитие, прежде чем пойти через
	// ретранслятор. Прямой путь всегда лучше (быстрее и не грузит чужой сервер),
	// поэтому сначала пробуем пробиться и только потом сдаёмся.
	relayGrace    = 6 * time.Second
	relayBindTick = 20 * time.Second // как часто напоминаем о себе ретранслятору

	// addrGossipTick — как часто шлём подтверждённым пирам FrameAddr: свои
	// кандидаты + их reflex-адрес. Дёшево (P2P, без сервера) и держит связь при
	// смене адреса, не дожидаясь сигналки.
	addrGossipTick = 15 * time.Second
	maxCandidates  = 12 // потолок кандидатов на пира: чужой госсип не должен раздуть список

	// pexGossipTick — как часто шлём подтверждённым пирам FramePeers (адреса
	// ДРУГИХ подтверждённых прямых пиров той же сети). Реже addrGossipTick: это
	// уже вторичное распространение — транзитивная связность выигрывает и от
	// более редкой рассылки, а растущий список пиров дороже пары своих кандидатов.
	pexGossipTick = 30 * time.Second
	// maxPexEntries — потолок записей в кадре PEX. 16×19+1 = 305 байт худшего
	// случая (все IPv6) — с запасом влезает в MTU 1280.
	maxPexEntries = 16

	// Живой переопрос STUN на боевом сокете — чтобы внешний адрес не замерзал на
	// стартовом значении: NAT со временем перевешивает порт, и старый адрес ломает
	// прямой путь до самого реконнекта. Часто, пока путь деградировал (есть relay/
	// suspect/непробитый пир — тогда важно быстро переоткрыть свой адрес), редко,
	// когда всё стабильно (ловить тихие ребинды).
	stunRecheckFast  = 20 * time.Second
	stunRecheckSlow  = 2 * time.Minute
	stunProbeTimeout = 5 * time.Second // ждём ответ; позже — чистим повисший txID
)

// Обнаружение без сигналки: голые адреса (DHT отдаёт IP:port без PeerID) и пиры,
// узнанные из входящего кадра.
const (
	// probeTTL — сколько долбим голый адрес-кандидат, прежде чем забыть. DHT-запись
	// живёт около двух часов, но протухший адрес долбить смысла нет: источник
	// подсыпает свежие каждый раунд поиска.
	probeTTL  = 5 * time.Minute
	maxProbes = 64 // потолок голых адресов на сеть: в DHT кто угодно может анонсировать мусор

	// maxLearnedPeers — потолок пиров, созданных из входящего трафика (а не из
	// ответа сигналки). Подделать кадр без ключа сети нельзя, так что это защита не
	// от чужого, а от разрастания таблицы на повторах и от участника-вредителя.
	maxLearnedPeers = 64
	// learnedExpire — узнанного из трафика пира забываем, если он столько молчит.
	// Сигналка про него ничего не знает (в DHT-режиме её нет вовсе), поэтому чистит
	// его только maintenance.
	learnedExpire = 10 * time.Minute

	// probeBackoffBase/Max — экспоненциальный backoff пробития голого адреса из DHT.
	// Первые попытки часто (быстро сводим настоящего пира), дальше реже (мусорный
	// адрес не долбим впустую всё его время жизни). Сравни: подтверждённый пир
	// пингуется по фиксированному pingInterval — там адрес доверенный.
	probeBackoffBase = 2 * time.Second
	probeBackoffMax  = 30 * time.Second
)

// probeBackoff — пауза перед следующим пробитием голого адреса по числу уже
// сделанных попыток: base, 2×base, 4×base … но не больше probeBackoffMax.
func probeBackoff(tries int) time.Duration {
	d := probeBackoffBase
	for i := 0; i < tries && d < probeBackoffMax; i++ {
		d *= 2
	}
	if d > probeBackoffMax {
		d = probeBackoffMax
	}
	return d
}

// Типы пакетов ретранслятора (см. cmd/lanmesh-relay).
const (
	relayBind    byte = 0x01
	relayData    byte = 0x02
	relayForward byte = 0x03
	relayBindOK  byte = 0x04

	relayTagLen = 32
)

// network — одна mesh-сеть внутри общего движка: свой ключ (sealer), тег и своя
// таблица пиров. Сокет, Wintun-адаптер и внешний адрес (STUN/relay/reflex) —
// общие на все сети, см. Engine. Один физический пир, состоящий сразу в двух
// наших сетях, попадёт в обе таблицы как отдельный peerState — так проще и
// корректно (каждый пробьётся со своего, но с общего сокета).
type network struct {
	tag    [relayTagLen]byte
	sealer *crypto.Sealer
	name   string
	peers  map[proto.PeerID]*peerState
	byIP   map[netip.Addr]*peerState

	// useRelay — можно ли этой сети пользоваться ретранслятором. Адрес релея общий
	// на узел, но разрешение — у каждой сети своё: сеть в режиме «без серверов»
	// не должна даже представляться ретранслятору (он увидел бы её тег), а для
	// обычных сетей это по-прежнему запасной путь при неудачном пробитии.
	useRelay bool

	// probes — голые адреса-кандидаты без PeerID (от DHT-обнаружения). Долбим их
	// пробитием: у кого есть ключ сети, тот ответит, и пир создастся из его кадра
	// (см. learnPeerLocked). Ключ — addr.String(), значение — когда добавлен.
	probes map[string]probeAddr

	// sendCtr — исходящий счётчик кадров (anti-replay), общий для всех получателей в
	// этой сети (мы — единственный отправитель со своим PeerID). Засеян временем
	// старта, а не нулём: после перезапуска узла счётчик стартует ЗАВЕДОМО выше, чем
	// был у нас в прошлой сессии, — иначе приёмник у пиров отбросил бы наши кадры как
	// «старые». Атомарный: seal зовут из нескольких горутин.
	sendCtr atomic.Uint64
}

// probeAddr — голый адрес-кандидат и момент, когда мы о нём узнали.
type probeAddr struct {
	addr     netip.AddrPort
	added    time.Time // момент ПЕРВОГО обнаружения — от него абсолютный probeTTL
	tries    int       // сколько раз уже долбили (для экспоненциального backoff)
	lastPoke time.Time // когда долбили в последний раз
}

type peerState struct {
	net       *network // сеть, которой принадлежит пир (её ключом шифруем трафик к нему)
	id        proto.PeerID
	name      string
	// learned — пир создан из входящего кадра, а не из ответа сигналки. Такого
	// сигналка не «ведёт»: чистит его по молчанию maintenance (learnedExpire).
	learned bool
	virtualIP netip.Addr
	endpoints []netip.AddrPort // кандидаты (STUN + локальные)
	active    netip.AddrPort   // подтверждён по входящему пакету — ТОЛЬКО прямой путь
	lastRecv  time.Time        // когда пришёл прямой пакет
	firstSeen time.Time        // когда узнали о пире — от него отсчитываем relayGrace

	// Ретранслятор. lastRelayRecv отдельно от lastRecv: пакет через relay НЕ
	// подтверждает прямой endpoint, иначе мы бы считали дырку пробитой и слали
	// данные в никуда.
	lastRelayRecv time.Time

	// absentSince — когда пир пропал из ответа сигналки; ноль = он там есть.
	// Нужен, чтобы не сносить пира по одной осечке сигналки, см. SyncPeers.
	absentSince time.Time

	// Anti-replay: скользящее окно принятых счётчиков этого отправителя. recvMax —
	// наибольший принятый счётчик, recvBits — битовая маска последних 64 (бит i =
	// принят ли recvMax-i). См. acceptCounter.
	recvMax  uint64
	recvBits uint64

	// Замер задержки. pingAt — момент отправки ping с номером pingSeq; обнуляется
	// при получении ответа. Время только локальное (монотонное), часы пиров не
	// участвуют.
	pingSeq  uint64
	pingAt   time.Time
	pingSent time.Time
	rtt      time.Duration // 0 = ещё не измерен
	rttAt    time.Time     // когда измерен — чтобы не показывать протухший

	// Счётчики данных (FrameData/FrameBroadcast): всего отправлено пиру / принято
	// от него, байт полезной нагрузки. Атомарные — обновляются в горячих путях
	// send/recv без общего лока движка (см. sendFrame/sendToAll/netToTun).
	bytesTx atomic.Uint64
	bytesRx atomic.Uint64
}

// Engine связывает TUN и UDP-сокет, ведёт таблицы пиров ПО СЕТЯМ и гоняет между
// ними трафик. Сокет, адаптер и внешний адрес — общие; сети (ключ+тег+пиры)
// добавляются/убираются на ходу через AddNetwork/RemoveNetwork.
type Engine struct {
	conn   *net.UDPConn
	tun    TUN
	selfID proto.PeerID
	selfIP netip.Addr

	// selfName — наше отображаемое имя, которое рассылаем пирам в FrameHello.
	// Под mu, потому что пользователь может переименовать узел на ходу.
	selfName string

	// Ретранслятор общий (адрес один); тег у каждой сети свой, см. network.
	// Пустое значение (netip.AddrPort{}) = релей не задан, проверка — IsValid.
	relay netip.AddrPort

	mu   sync.RWMutex
	nets map[[relayTagLen]byte]*network // сети по тегу

	// Обмен адресами напрямую (FrameAddr), см. addrGossipTick.
	selfCands  []string      // наши кандидаты для рассылки пирам; ставит app.Session
	selfReflex string        // наш внешний адрес, каким его видят пиры (peer-reflexive)
	reflexCh   chan struct{} // сигнал сессии: reflex изменился — пора перерегистрироваться

	// relayReflex — наш внешний адрес, каким его видит РЕТРАНСЛЯТОР на боевом
	// сокете (из расширенного bind-ack). По сути STUN, но от нашего сервера:
	// его не режет DPI и не отравляет VPN, как публичный STUN. Запасной внешний
	// адрес, когда STUN молчит, и опорная точка для сверки.
	relayReflex string

	// Живой переопрос STUN на боевом сокете (см. probeSTUN/matchSTUN). stunReflex —
	// последний внешний адрес от STUN; stunPending — txID отправленных запросов,
	// по ним readLoop опознаёт ответы (сокет общий с трафиком пиров).
	stunServers []string
	stunReflex  string
	stunPending map[[12]byte]time.Time

	// onDirect — наружу сообщаем ТОЛЬКО подтверждённый прямой адрес (пришёл
	// расшифрованный кадр). Кандидаты не годятся: кэш накопил бы мусор из DHT.
	onDirect func(tag [relayTagLen]byte, id proto.PeerID, addr netip.AddrPort)
}

// NewEngine создаёт движок на готовом UDP-сокете и TUN-устройстве. Сети
// добавляются потом через AddNetwork (движок работает и без единой сети —
// поднятый адаптер сам по себе безвреден).
func NewEngine(conn *net.UDPConn, tun TUN, selfID proto.PeerID, selfIP netip.Addr) *Engine {
	return &Engine{
		conn:   conn,
		tun:    tun,
		selfID: selfID,
		selfIP: selfIP,
		nets:   make(map[[relayTagLen]byte]*network),
	}
}

// AddNetwork подключает сеть с разрешённым ретранслятором (обычный случай). Тонкая
// обёртка над AddNetworkRelay для совместимости и краткости в тестах/CLI.
func (e *Engine) AddNetwork(tag [relayTagLen]byte, sealer *crypto.Sealer, name string) {
	e.AddNetworkRelay(tag, sealer, name, true)
}

// AddNetworkRelay подключает сеть (ключ+тег+имя) на ходу, СРАЗУ задавая разрешение
// на ретранслятор. Идемпотентно: повторный вызов с тем же тегом обновляет
// ключ/имя/флаг, таблицу пиров сохраняет. Потокобезопасно.
//
// Флаг задаётся здесь, под тем же локом, что и создание сети, — а не отдельным
// SetNetworkRelay следом: иначе между двумя вызовами оставалось окно, в котором тик
// maintenance мог увидеть сеть без серверов как разрешающую релей и представить её
// ретранслятору (тот узнал бы её тег).
func (e *Engine) AddNetworkRelay(tag [relayTagLen]byte, sealer *crypto.Sealer, name string, allowRelay bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n := e.nets[tag]; n != nil {
		n.sealer = sealer
		n.name = name
		n.useRelay = allowRelay
		return
	}
	nw := &network{
		tag:      tag,
		sealer:   sealer,
		name:     name,
		useRelay: allowRelay,
		peers:    make(map[proto.PeerID]*peerState),
		byIP:     make(map[netip.Addr]*peerState),
		probes:   make(map[string]probeAddr),
	}
	nw.sendCtr.Store(uint64(time.Now().UnixNano())) // засев выше прошлой сессии
	e.nets[tag] = nw
}

// RemoveNetwork отключает сеть по тегу вместе с её таблицей пиров.
func (e *Engine) RemoveNetwork(tag [relayTagLen]byte) {
	e.mu.Lock()
	delete(e.nets, tag)
	e.mu.Unlock()
}

// netByTag — сеть по тегу (nil, если такой нет). Вызывать под локом.
func (e *Engine) netByTag(tag [relayTagLen]byte) *network {
	return e.nets[tag]
}

// SetSelfName задаёт наше отображаемое имя для рассылки пирам (FrameHello).
// Пустое имя не шлём — пир оставит нас безымянным и покажет по виртуальному IP.
func (e *Engine) SetSelfName(name string) {
	if len(name) > proto.HelloMaxLen {
		name = name[:proto.HelloMaxLen]
	}
	e.mu.Lock()
	e.selfName = name
	e.mu.Unlock()
}

// OnDirectConfirmed ставит колбэк, дёргаемый при подтверждении прямого пути к
// пиру. Зовётся из горячего пути чтения — колбэк обязан быть быстрым и не
// блокировать (запись в кэш идёт в память, на диск сохраняет таймер сессии).
func (e *Engine) OnDirectConfirmed(fn func(tag [relayTagLen]byte, id proto.PeerID, addr netip.AddrPort)) {
	e.mu.Lock()
	e.onDirect = fn
	e.mu.Unlock()
}

// SetNetworkRelay разрешает или запрещает сети пользоваться ретранслятором.
// Запрет полный: ни bind (значит, релей не узнает даже тега сети), ни отправка
// через него, ни приём. Вызывать сразу после AddNetwork.
func (e *Engine) SetNetworkRelay(tag [relayTagLen]byte, allowed bool) {
	e.mu.Lock()
	if n := e.nets[tag]; n != nil {
		n.useRelay = allowed
	}
	e.mu.Unlock()
}

// AddProbes добавляет в сеть голые адреса-кандидаты (IP:port без PeerID) — их
// отдаёт обнаружение через DHT, где записи несут только адрес. Движок будет
// долбить их пробитием; ответит только тот, у кого есть ключ сети, и уже из его
// кадра появится настоящий пир (learnPeerLocked). Дубли и мусор безвредны:
// незашифрованный нашим ключом ответ никого не создаёт.
//
// Список сознательно НЕ идёт в endpoints пиров: там потолок maxCandidates=12, и
// мусор из публичной DHT вытеснил бы рабочие адреса от сигналки/госсипа.
func (e *Engine) AddProbes(tag [relayTagLen]byte, addrs []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := e.nets[tag]
	if n == nil {
		return
	}
	now := time.Now()
	for _, s := range addrs {
		// ParseAddrPort, а не ResolveUDPAddr: адрес из DHT — всегда литерал ip:port,
		// а резолвер ходил бы в DNS прямо под e.mu на любой мусор из публичной сети.
		a, err := netip.ParseAddrPort(s)
		if err != nil || a.Port() == 0 {
			continue
		}
		// Адрес чужой (кадр DHT), может прийти в mapped-форме (::ffff:a.b.c.d).
		// Внутри движка mapped-адресов быть не должно: Unmap реального IPv6 не
		// трогает, а mapped IPv4 схлопывает с его же unmapped-формой — иначе один
		// физический адрес держал бы два слота в maxProbes и не дедуплицировался.
		a = netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
		k := a.String()
		if _, ok := n.probes[k]; ok {
			// Уже знаем этот адрес — НЕ обновляем added и не сбрасываем backoff.
			// Иначе probeTTL превращался бы в скользящее окно: адрес, который
			// раз за разом возвращает публичная DHT (в т.ч. чужой мусор или
			// подставленный адрес-жертва), долбился бы вечно.
			continue
		}
		if len(n.probes) >= maxProbes {
			continue
		}
		n.probes[k] = probeAddr{addr: a, added: now}
	}
}

// ProbeCount — сколько голых адресов сейчас в работе (для панели/диагностики).
func (e *Engine) ProbeCount(tag [relayTagLen]byte) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	n := e.nets[tag]
	if n == nil {
		return 0
	}
	return len(n.probes)
}

// UseRelay задаёт адрес ретранслятора — общий на все сети (он ведёт таблицу по
// паре (тег, peerID), а тег у каждой сети свой). Расшифровать он ничего не может.
func (e *Engine) UseRelay(addr netip.AddrPort) {
	// Нормализуем здесь, а не полагаемся на вызывающего: netToTun сверяет адрес
	// отправителя с релеем побайтово (src == relay) уже в unmapped-форме, и любой
	// новый вызывающий, забывший Unmap у себя, тихо сломал бы всю пересылку через
	// ретранслятор — ни один кадр от него не прошёл бы сверку.
	e.mu.Lock()
	e.relay = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
	e.mu.Unlock()
}

// relayAddr отдаёт адрес ретранслятора (пустой AddrPort, если не задан).
func (e *Engine) relayAddr() netip.AddrPort {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.relay
}

// SetSelfCandidates задаёт наши кандидаты для рассылки пирам через FrameAddr.
// Обновляется app.Session каждый раунд регистрации свежим списком.
func (e *Engine) SetSelfCandidates(cands []string) {
	e.mu.Lock()
	e.selfCands = append(e.selfCands[:0:0], cands...)
	e.mu.Unlock()
}

// SelfReflex — наш внешний адрес, каким его видят пиры (learned из FrameAddr).
// ok=false, пока ни один пир его не сообщил. Для симметричного NAT адрес свой на
// каждого пира, но там прямой путь и не открывается — а где открывается (cone),
// адрес один, и reflex верен.
func (e *Engine) SelfReflex() (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.selfReflex, e.selfReflex != ""
}

// RelayReflex — наш внешний адрес глазами ретранслятора (IP:port на боевом
// сокете). ok=false, пока релей не прислал расширенный bind-ack (старый релей его
// не шлёт). Служит запасным внешним адресом при заблокированном STUN и опорой
// для сверки «STUN не отравлен ли VPN».
func (e *Engine) RelayReflex() (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.relayReflex, e.relayReflex != ""
}

// setRelayReflex запоминает наш адрес глазами релея и, если он СМЕНИЛСЯ, будит
// перерегистрацию: релей видит наш текущий src:port каждые 20с независимо от
// пиров, поэтому именно он ловит смену внешнего порта (NAT перевесил mapping),
// когда прямой госсип уже оборвался. Без этого новый порт уезжал на сигналку
// только со следующим тиком, а то и вовсе замерзал до реконнекта.
func (e *Engine) setRelayReflex(addr string) {
	e.mu.Lock()
	changed := addr != "" && addr != e.relayReflex
	if changed {
		e.relayReflex = addr
	}
	ch := e.reflexCh
	e.mu.Unlock()

	if changed && ch != nil {
		select {
		case ch <- struct{}{}:
		default: // сигнал уже висит — одного достаточно
		}
	}
}

// validEndpoint — строка вида "ip:port" с валидным IPv4/IPv6 и портом. Пакет от
// релея доверенный (пришёл с его адреса), но мусор в reflex пускать всё равно ни к чему.
func validEndpoint(s string) bool {
	host, port, err := net.SplitHostPort(s)
	return err == nil && net.ParseIP(host) != nil && port != ""
}

// SetSTUNServers задаёт серверы для живого переопроса внешнего адреса. Вызывать
// до Run (или на ходу — потокобезопасно).
func (e *Engine) SetSTUNServers(servers []string) {
	e.mu.Lock()
	e.stunServers = append(e.stunServers[:0:0], servers...)
	if e.stunPending == nil {
		e.stunPending = make(map[[12]byte]time.Time)
	}
	e.mu.Unlock()
}

// StunReflex — наш внешний адрес по последнему живому переопросу STUN. ok=false,
// пока ни один ответ не пришёл.
func (e *Engine) StunReflex() (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.stunReflex, e.stunReflex != ""
}

func (e *Engine) setStunReflex(addr string) {
	e.mu.Lock()
	changed := addr != "" && addr != e.stunReflex
	if changed {
		e.stunReflex = addr
	}
	ch := e.reflexCh
	e.mu.Unlock()
	if changed && ch != nil {
		select {
		case ch <- struct{}{}: // смена внешнего порта — будим перерегистрацию
		default:
		}
	}
}

// probeSTUN шлёт Binding Request на все STUN-серверы с БОЕВОГО сокета; ответы
// ловит readLoop (matchSTUN) и обновляет stunReflex. Так внешний адрес не
// замерзает: порт в ответе — тот же, что у реального туннеля.
func (e *Engine) probeSTUN() {
	e.mu.Lock()
	servers := append([]string(nil), e.stunServers...)
	for tx, at := range e.stunPending { // чистим повисшие ожидания
		if time.Since(at) > stunProbeTimeout {
			delete(e.stunPending, tx)
		}
	}
	e.mu.Unlock()

	for _, s := range servers {
		raddr, err := net.ResolveUDPAddr("udp4", s)
		if err != nil {
			continue
		}
		req, tx, err := signal.BuildSTUNRequest()
		if err != nil {
			continue
		}
		e.mu.Lock()
		e.stunPending[tx] = time.Now()
		e.mu.Unlock()
		e.conn.WriteToUDP(req, raddr)
	}
}

// matchSTUN пробует опознать пакет как ответ на наш STUN-запрос (по txID). Если
// да — обновляет stunReflex и возвращает true (пакет не пойдёт на расшифровку).
// Дёшево: при пустом stunPending (обычное состояние) сразу выходит.
func (e *Engine) matchSTUN(raw []byte) bool {
	e.mu.Lock()
	if len(e.stunPending) == 0 {
		e.mu.Unlock()
		return false
	}
	found := ""
	for tx := range e.stunPending {
		if addr, ok := signal.ParseSTUNResponse(raw, tx); ok {
			found = addr
			// Ответ получен — остальные ожидания больше не нужны.
			for t := range e.stunPending {
				delete(e.stunPending, t)
			}
			break
		}
	}
	e.mu.Unlock()
	if found == "" {
		return false
	}
	e.setStunReflex(found)
	return true
}

// ReflexNotify возвращает канал, в который движок шлёт сигнал, когда наш reflex-
// адрес изменился, — сессии пора перерегистрироваться немедленно, не дожидаясь
// тика. Вызывать до Run.
func (e *Engine) ReflexNotify() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.reflexCh == nil {
		e.reflexCh = make(chan struct{}, 1)
	}
	return e.reflexCh
}

// SyncPeers обновляет таблицу пиров из ответа сигналки: добавляет новых, обновляет
// списки endpoint'ов, удаляет пропавших. Активный endpoint и lastRecv сохраняем —
// их подтверждает только реальный трафик, а не сигналка.
//
// Пропавших забываем НЕ СРАЗУ, а спустя peerForget. Сигналка — это подсказка, а
// не истина: она может икнуть и вернуть пустой список (например, Durable Object
// выгрузили из памяти по простою). Раньше одна такая осечка сносила peerState
// вместе с пробитым endpoint'ом — живое соединение рвалось, движок начинал
// пробиваться заново и уходил на ретранслятор. Живой трафик от пира — довод
// весомее, чем мнение сигналки.
func (e *Engine) SyncPeers(tag [relayTagLen]byte, list []proto.PeerInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()

	n := e.nets[tag]
	if n == nil {
		return // сеть уже отключили — синхронизировать нечего
	}

	now := time.Now()
	seen := make(map[proto.PeerID]bool, len(list))
	for _, info := range list {
		id, err := proto.ParsePeerID(info.PeerID)
		if err != nil || id == e.selfID {
			continue
		}
		// VirtualIP считаем САМИ из PeerID, а не берём из ответа сигналки/пира: иначе
		// участник (или поддельная сигналка) мог бы назвать чужой vip и увести на
		// себя весь трафик к тому пиру. Присланный info.VirtualIP игнорируем.
		vip := proto.VirtualIP(id)
		seen[id] = true

		eps := make([]netip.AddrPort, 0, len(info.Endpoints))
		for _, s := range info.Endpoints {
			// Кандидаты от сигналки — литералы ip:port (их собирает localEndpoints
			// и STUN), поэтому парсер, а не резолвер: в DNS под локом не ходим.
			if a, err := netip.ParseAddrPort(s); err == nil {
				// Список Endpoints пришёл от чужого узла через сигналку — нормализуем
				// mapped-форму, см. AddProbes.
				a = netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
				eps = append(eps, a)
			}
		}

		ps := n.peers[id]
		if ps == nil {
			// firstSeen — точка отсчёта relayGrace: сколько даём на честное
			// пробитие, прежде чем идти в обход через ретранслятор.
			ps = &peerState{net: n, id: id, firstSeen: now}
			n.peers[id] = ps
		}
		// Пир подтверждён сигналкой — он больше НЕ «learned». Если он был впервые
		// заведён из входящего трафика (learnPeerLocked, learned=true) до первого
		// ответа сигналки, а теперь пришёл в её списке, оставить learned=true значило
		// бы отдать его под чистку learnedExpire (10м молчания по трафику) вопреки
		// тому, что сигналка его подтверждает, — то есть сносить рабочего пира.
		ps.learned = false
		ps.absentSince = time.Time{} // снова в списке
		ps.name = info.Name
		ps.virtualIP = vip
		// НЕ затираем список кандидатов пустым/укороченным ответом сигналки: глюк
		// хранилища на сервере (пир виден, но Endpoints пуст) иначе стёр бы и
		// P2P-кандидаты от гостипа — единственный рабочий путь при symmetric NAT.
		// Свежий список от сигналки идёт первым, прежние адреса добираются следом.
		ps.endpoints = mergeEndpoints(eps, ps.endpoints)
		n.byIP[vip] = ps
	}

	for id, ps := range n.peers {
		if seen[id] {
			continue
		}
		if ps.learned {
			continue // не из сигналки и не ей его сносить, см. learnedExpire
		}
		// Живой трафик весомее мнения сигналки. Без этой проверки достаточно было
		// одному участнику перестать регистрироваться (перешёл в режим без
		// сигналки, ушёл в оффлайн-режим обнаружения) — и остальные сносили ему
		// РАБОЧЕЕ соединение через 90с, хотя пакеты от него шли всё это время.
		if now.Sub(ps.lastHeard()) < peerForget {
			continue
		}
		if ps.absentSince.IsZero() {
			ps.absentSince = now
			continue // первая осечка — ждём, вдруг сигналка просто моргнула
		}
		if now.Sub(ps.absentSince) >= peerForget {
			delete(n.byIP, ps.virtualIP)
			delete(n.peers, id)
		}
	}
}

// Run запускает движок: чтение TUN->сеть, чтение сети->TUN и обслуживание NAT.
// Блокируется до ошибки одного из циклов.
func (e *Engine) Run() error {
	errc := make(chan error, 2)
	// done останавливает maintenance вместе с движком. Без этого горутина
	// переживала отключение сети и продолжала тикать в закрытый сокет — вечно,
	// и по одной лишней на каждое переподключение.
	done := make(chan struct{})
	defer close(done)

	go func() { errc <- e.tunToNet() }()
	go func() { errc <- e.netToTun() }()
	go e.maintenance(done)
	return <-errc
}

// tunToNet читает исходящие IP-пакеты из TUN и рассылает их нужному пиру (или всем
// при broadcast/multicast).
func (e *Engine) tunToNet() error {
	buf := make([]byte, maxPacket)
	for {
		n, err := e.tun.Read(buf)
		if err != nil {
			return err
		}
		pkt := buf[:n]
		dst, ok := ipv4Dst(pkt)
		if !ok {
			continue // не IPv4 — MVP игнорирует
		}

		if isBroadcastLike(dst) {
			// Широковещалка/мультикаст: LAN-игры так ищут друг друга. Дублируем
			// пакет юникастом каждому пиру — на их стороне он придёт в TUN как
			// обычный broadcast, и игра увидит «соседа по локалке».
			e.sendToAll(proto.FrameBroadcast, pkt)
			continue
		}

		e.mu.RLock()
		var ps *peerState
		for _, nw := range e.nets { // ищем пира с этим IP среди всех сетей
			if p := nw.byIP[dst]; p != nil {
				ps = p
				break
			}
		}
		e.mu.RUnlock()
		if ps == nil {
			continue // адрес ни из одной нашей сети — дропаем
		}
		e.sendFrame(ps, proto.FrameData, pkt)
	}
}

// netToTun читает кадры пиров из UDP, расшифровывает и обрабатывает по типу.
func (e *Engine) netToTun() error {
	buf := make([]byte, 2048)
	for {
		n, srcAP, err := e.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return err
		}
		// Нормализация ровно здесь и больше нигде: дальше по коду mapped-адресов
		// не существует, поэтому сверка с relay, дедуп probes и подтверждение
		// endpoint'а работают с одной формой. Проверено: v4 через dual-stack сокет
		// приходит как ::ffff:a.b.c.d, Is4()==false.
		src := netip.AddrPortFrom(srcAP.Addr().Unmap(), srcAP.Port())

		raw := buf[:n]
		viaRelay := false
		if relay := e.relayAddr(); relay.IsValid() && src == relay {
			// От ретранслятора приходит либо подтверждение bind, либо кадр в
			// обёртке — снимаем её и дальше всё как обычно.
			if len(raw) < 1 {
				continue
			}
			switch raw[0] {
			case relayBindOK:
				// Ретранслятор жив. Расширенный ack несёт адрес, с которого релей
				// видит наш БОЕВОЙ сокет, — готовый внешний endpoint от нашего
				// сервера (не отравить/не заблокировать, как публичный STUN).
				// Старый релей хвоста не шлёт — тогда парсить нечего.
				const off = 1 + relayTagLen + len(proto.PeerID{}) // [тип][тег][peerID]
				if len(raw) > off {
					if s := string(raw[off:]); validEndpoint(s) {
						e.setRelayReflex(s)
					}
				}
				continue
			case relayForward:
				raw = raw[1:]
				viaRelay = true
			default:
				continue
			}
		}

		// Ответ на наш живой переопрос STUN (не пир и не relay — это внешний адрес
		// нашего боевого сокета). Ловим до расшифровки: sealer его не откроет, а так
		// адрес не потеряется.
		if !viaRelay && e.matchSTUN(raw) {
			continue
		}

		// Какой сети пакет? Перебираем ключи: AEAD расшифрует только своим.
		// Снимок сетей берём под локом, а сам Open (CPU) делаем без лока.
		e.mu.RLock()
		netsSnap := make([]*network, 0, len(e.nets))
		for _, cand := range e.nets {
			netsSnap = append(netsSnap, cand)
		}
		e.mu.RUnlock()

		var plain []byte
		var nw *network
		for _, cand := range netsSnap {
			if p, err := cand.sealer.Open(raw); err == nil && len(p) >= frameHeader {
				plain, nw = p, cand
				break
			}
		}
		if nw == nil {
			continue // ничей ключ / мусор / обрезка — молча дропаем
		}
		if viaRelay {
			// Сети, которой релей запрещён, через него ничего не приходит и приходить
			// не должно (мы там не биндимся). Кадр в такой обёртке — либо остаток
			// прошлой сессии, либо чужая игра: дропаем, чтобы запрет был полным.
			e.mu.RLock()
			allowed := nw.useRelay
			e.mu.RUnlock()
			if !allowed {
				continue
			}
		}

		var senderID proto.PeerID
		copy(senderID[:], plain[1:frameCounterOff])
		typ := plain[0]
		counter := binary.BigEndian.Uint64(plain[frameCounterOff:frameHeader])
		payload := plain[frameHeader:]

		e.mu.Lock()
		ps := nw.peers[senderID]
		learnedNow := false
		if ps == nil {
			// Пира нет в таблице — но кадр РАСШИФРОВАЛСЯ ключом сети, а это
			// криптографическое доказательство членства (ключ = KDF(имя+пароль)).
			// Раньше такой кадр дропался «сигналка ещё не отдала» — и это делало
			// сигналку обязательной: узнать пира было больше неоткуда. Теперь
			// узнаём его прямо отсюда, и обнаружение (DHT, кэш, инвайт с адресом)
			// может обойтись вообще без сервера. Виртуальный IP считаем сами из
			// PeerID, поэтому подменить чужой адрес отправитель не может.
			ps = e.learnPeerLocked(nw, senderID)
			learnedNow = ps != nil
		}
		if ps == nil {
			e.mu.Unlock()
			continue // свои же кадры / потолок узнанных пиров
		}
		if !ps.acceptCounter(counter) {
			e.mu.Unlock()
			continue // повтор/устаревший кадр (anti-replay) — молча дропаем
		}
		if viaRelay {
			// Через ретранслятор: пир жив, но прямая дырка НЕ пробита.
			// Записать src в active было бы ошибкой — это адрес relay, и мы
			// бы решили, что пробились, перестав долбить кандидаты.
			ps.lastRelayRecv = time.Now()
		} else {
			// changed — адрес сменился или подтверждается впервые. Колбэк наружу
			// (кэш endpoint'ов) дёргаем только на нём, а не на каждом пакете — он
			// в горячем пути чтения.
			changed := !ps.active.IsValid() || ps.active != src
			// Прямой валидный пакет — вот он, итог пробития NAT.
			ps.active = src
			ps.lastRecv = time.Now()
			// Адрес, с которого пир до нас достучался, — рабочий кандидат: держим
			// его в списке, чтобы после молчания было куда перепробиваться.
			mergeCandidates(ps, []string{src.String()})
			delete(nw.probes, src.String()) // голый адрес отработал, стал пиром
			if changed && e.onDirect != nil {
				e.onDirect(nw.tag, ps.id, src)
			}
		}
		selfName := e.selfName
		e.mu.Unlock()

		if learnedNow {
			// Только что познакомились: представляемся, иначе в панели у обоих
			// будет безымянный пир (имена раздавала сигналка, а её может не быть).
			if selfName != "" {
				if viaRelay {
					e.writeFrameRelay(nw, ps.id, proto.FrameHello, []byte(selfName))
				} else {
					e.writeFrame(nw, src, proto.FrameHello, []byte(selfName))
				}
			}
		}

		switch typ {
		case proto.FrameData, proto.FrameBroadcast:
			if len(payload) > 0 {
				ps.bytesRx.Add(uint64(len(payload)))
				_, _ = e.tun.Write(payload)
			}
		case proto.FramePunch:
			// Пакет пробития/keepalive: endpoint уже отмечен выше, тела нет.
		case proto.FramePing:
			// Эхо: возвращаем номер как есть, ТЕМ ЖЕ путём, откуда пришёл ping.
			// Прямой ответ шлём на src, а не на ps.active: пир мог сменить
			// endpoint. Пришедший через relay нельзя слать на src напрямую —
			// это адрес ретранслятора, ему нужна обёртка с адресатом.
			if len(payload) >= 8 {
				if viaRelay {
					e.writeFrameRelay(nw, ps.id, proto.FramePong, payload)
				} else {
					e.writeFrame(nw, src, proto.FramePong, payload)
				}
			}
		case proto.FramePong:
			if len(payload) >= 8 {
				e.notePong(ps, binary.BigEndian.Uint64(payload))
			}
		case proto.FrameAddr:
			// Пришёл через relay — reflex бессмыслен (src = ретранслятор), берём
			// только кандидаты; напрямую — и reflex наш внешний адрес, и кандидаты.
			reflex, cands := decodeAddr(payload)
			if viaRelay {
				reflex = ""
			}
			e.handleAddr(ps, reflex, cands)
		case proto.FrameHello:
			// Пир назвал себя. Режем длину на приёме и не даём пустым именем
			// стереть уже известное (от сигналки оно приходит вместе с пиром).
			name := string(payload)
			if len(name) > proto.HelloMaxLen {
				name = name[:proto.HelloMaxLen]
			}
			if name != "" {
				e.mu.Lock()
				ps.name = name
				e.mu.Unlock()
			}
		case proto.FramePeers:
			// Транзитивный PEX: пир прислал адреса СВОИХ подтверждённых прямых
			// пиров. Прямо в общий пул проб — там уже есть дедуп, потолок
			// maxProbes и отдельная от endpoints очередь. Настоящим пиром адрес
			// станет только когда придёт кадр, расшифрованный ключом сети, — как
			// с DHT, PeerID тут ничего не решает.
			e.mu.RLock()
			selfCands := append([]string(nil), e.selfCands...)
			e.mu.RUnlock()
			pexAddrs := make([]string, 0, maxPexEntries)
			for _, ap := range decodePeers(payload) {
				s := ap.String()
				own := false
				for _, c := range selfCands {
					if c == s {
						own = true // свой же адрес (глазами пира) долбить незачем
						break
					}
				}
				if !own {
					pexAddrs = append(pexAddrs, s)
				}
			}
			if len(pexAddrs) > 0 {
				e.AddProbes(nw.tag, pexAddrs)
			}
		}
	}
}

// learnPeerLocked заводит пира, узнанного из входящего кадра (а не от сигналки).
// Возвращает nil, если это мы сами или упёрлись в потолок. Вызывать под e.mu.Lock.
//
// Имя оставляем пустым: его сообщит сам пир в FrameHello. VirtualIP считаем из
// PeerID — он и только он определяет адрес в виртуальной сети.
func (e *Engine) learnPeerLocked(n *network, id proto.PeerID) *peerState {
	if id == e.selfID {
		return nil // свой же кадр вернулся (например, петля через relay) — не пир
	}
	learned := 0
	for _, p := range n.peers {
		if p.learned {
			learned++
		}
	}
	if learned >= maxLearnedPeers {
		return nil
	}
	vip := proto.VirtualIP(id)
	ps := &peerState{net: n, id: id, virtualIP: vip, firstSeen: time.Now(), learned: true}
	n.peers[id] = ps
	n.byIP[vip] = ps
	return ps
}

// notePong принимает ответ на наш ping и считает RTT. Ответ на устаревший номер
// (наш ping потерялся, пришёл дубль) игнорируем — иначе задержка будет завышена.
func (e *Engine) notePong(ps *peerState, seq uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ps.pingAt.IsZero() || ps.pingSeq != seq {
		return
	}
	ps.rtt = time.Since(ps.pingAt)
	ps.rttAt = time.Now()
	ps.pingAt = time.Time{}
}

// maintenance периодически пробивает NAT и пингует подтверждённых пиров.
//
// Для подтверждённого пира ping заменяет отдельный keepalive: это такой же UDP-
// пакет в тот же endpoint, дырку он держит не хуже, а заодно меряет задержку.
func (e *Engine) maintenance(done <-chan struct{}) {
	ticker := time.NewTicker(punchInterval)
	defer ticker.Stop()

	// Задания на отправку копим под локом, а шлём уже без него: сеть под мьютексом
	// держать нельзя, а ping требует записи в peerState (номер и время). Каждое
	// задание несёт свою сеть — её ключом шифруем, её тегом заворачиваем в relay.
	type pingJob struct {
		n   *network
		dst netip.AddrPort // прямой адрес; пустой = через ретранслятор
		id  proto.PeerID
		seq uint64
	}
	type punchJob struct {
		n   *network
		dst netip.AddrPort
	}
	type gossipJob struct {
		n      *network
		dst    netip.AddrPort // прямой адрес; пустой = через ретранслятор
		id     proto.PeerID
		reflex string // адрес пира, каким мы его видим ("" через relay)
	}
	// pexJob — рассылка FramePeers. payload общий на всю сеть (список ЕЁ
	// подтверждённых прямых пиров), поэтому кодируем его раз на сеть, а не на
	// каждого получателя.
	type pexJob struct {
		n       *network
		dst     netip.AddrPort // прямой адрес; пустой = через ретранслятор
		id      proto.PeerID
		payload []byte
	}

	var lastBind, lastGossip, lastPex, lastStun time.Time
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}

		now := time.Now()
		var pings []pingJob
		var punches []punchJob
		var gossips []gossipJob
		var pexes []pexJob
		gossipDue := now.Sub(lastGossip) >= addrGossipTick
		pexDue := now.Sub(lastPex) >= pexGossipTick

		e.mu.Lock()
		hasRelay := e.relay.IsValid()
		hasStun := len(e.stunServers) > 0
		degraded := false // хоть один пир не на свежем прямом пути → чаще переспрашиваем свой адрес
		cands := append([]string(nil), e.selfCands...)
		selfName := e.selfName
		netsList := make([]*network, 0, len(e.nets))
		for _, nw := range e.nets {
			// Разрешение на релей — у каждой сети своё, поэтому и список для bind, и
			// признак «есть запасной путь» считаем по сети, а не по узлу.
			nwRelay := hasRelay && nw.useRelay
			if nwRelay {
				netsList = append(netsList, nw)
			}

			// PEX-полезная нагрузка этой сети: адреса ЕЁ подтверждённых прямых
			// пиров (не кандидатов и не relay-only — иначе мусор размножается).
			// Считаем раз на сеть, а не на каждого получателя: рассылка всем её
			// подтверждённым пирам получает один и тот же список.
			var pexPayload []byte
			if pexDue {
				var direct []netip.AddrPort
				for _, ps := range nw.peers {
					if ps.active.IsValid() && now.Sub(ps.lastRecv) < peerTimeout {
						direct = append(direct, ps.active)
					}
				}
				if len(direct) > 0 {
					pexPayload = encodePeers(direct)
				}
			}

			// Голые адреса от обнаружения (DHT): долбим их пробитием. Ответит только
			// тот, у кого есть ключ сети, — и станет пиром. Долбим с экспоненциальным
			// backoff, а не каждый тик: адрес из ПУБЛИЧНОЙ DHT может быть мусором или
			// подставленным чужим адресом-жертвой, и слать ему пакет раз в 2с всё
			// время его жизни — это и лишний трафик, и превращение узла в инструмент
			// рефлективного флуда, и паттерн, который провайдер читает как скан.
			for k, pr := range nw.probes {
				if now.Sub(pr.added) >= probeTTL {
					delete(nw.probes, k)
					continue
				}
				if !pr.lastPoke.IsZero() && now.Sub(pr.lastPoke) < probeBackoff(pr.tries) {
					continue // ещё рано долбить этот адрес
				}
				pr.tries++
				pr.lastPoke = now
				nw.probes[k] = pr
				punches = append(punches, punchJob{n: nw, dst: pr.addr})
			}
			for id, ps := range nw.peers {
				// Пира, узнанного из трафика, никто не «ведёт»: сигналка про него не
				// знает, а в DHT-режиме её и нет. Замолчал надолго — забываем сами,
				// иначе таблица растёт от каждого случайного контакта.
				if ps.learned && now.Sub(ps.lastHeard()) >= learnedExpire {
					delete(nw.peers, id)
					delete(nw.byIP, ps.virtualIP)
					continue
				}
				confirmed := ps.active.IsValid() && now.Sub(ps.lastRecv) < peerTimeout
				relayPath := !confirmed && nwRelay && ps.usableRelay(now)
				// suspect — подтверждённый пир, но давно не слышно: возможно, у него
				// сменился адрес. Начинаем пробивать кандидаты заново, не дожидаясь
				// peerTimeout, — иначе без ретранслятора это минута отвала.
				suspect := ps.active.IsValid() && now.Sub(ps.lastRecv) > staleProbe
				if !confirmed || suspect || relayPath {
					degraded = true // путь к этому пиру не идеален — пора освежить свой адрес
				}

				// Gossip считаем ДО пинг-ветки: иначе её `continue` (когда пинг слали
				// недавно) проглатывал бы рассылку кандидатов, а lastGossip всё равно
				// сбрасывался — и обмен адресами растягивался вдвое против addrGossipTick.
				if gossipDue && len(cands) > 0 && (confirmed || relayPath) {
					if confirmed {
						gossips = append(gossips, gossipJob{n: nw, dst: ps.active, reflex: ps.active.String()})
					} else {
						gossips = append(gossips, gossipJob{n: nw, id: ps.id}) // через relay reflex не знаем
					}
				}
				// PEX — тем же получателям, что и gossip (подтверждённым прямым и
				// доступным через relay); содержимое уже отфильтровано выше до
				// подтверждённых прямых, отдельно от получателей.
				if pexDue && len(pexPayload) > 0 && (confirmed || relayPath) {
					if confirmed {
						pexes = append(pexes, pexJob{n: nw, dst: ps.active, payload: pexPayload})
					} else {
						pexes = append(pexes, pexJob{n: nw, id: ps.id, payload: pexPayload})
					}
				}

				if confirmed || relayPath {
					// Пингуем по тому пути, которым реально ходим: так RTT честно
					// показывает задержку рабочего маршрута, а не выдуманного.
					if now.Sub(ps.pingSent) < pingInterval {
						// ...но кандидаты долбить не перестаём, пока путь не надёжен.
						if !confirmed || suspect {
							for _, ep := range ps.endpoints {
								punches = append(punches, punchJob{n: nw, dst: ep})
							}
						}
						continue
					}
					ps.pingSeq++
					ps.pingAt = now
					ps.pingSent = now
					if confirmed {
						pings = append(pings, pingJob{n: nw, dst: ps.active, seq: ps.pingSeq})
					} else {
						pings = append(pings, pingJob{n: nw, id: ps.id, seq: ps.pingSeq})
					}
				}
				if !confirmed || suspect {
					// Прямой путь не открыт ИЛИ подтверждённый вдруг замолчал: долбим
					// ВСЕ кандидаты одновременно — встречные пакеты открывают дырку в
					// обоих NAT, и это же ловит смену адреса без ретранслятора.
					for _, ep := range ps.endpoints {
						punches = append(punches, punchJob{n: nw, dst: ep})
					}
				}
			}
		}
		e.mu.Unlock()

		if hasRelay && now.Sub(lastBind) >= relayBindTick {
			lastBind = now
			// netsList содержит только сети, которым релей разрешён: остальные не
			// должны светить ему даже свой тег.
			for _, nw := range netsList {
				e.sendRelayBind(nw)
			}
		}

		// Живой переопрос STUN: часто, пока путь деградировал (нужно быстро узнать
		// свой новый порт и переоткрыться), редко — когда всё стабильно.
		stunEvery := stunRecheckSlow
		if degraded {
			stunEvery = stunRecheckFast
		}
		if hasStun && now.Sub(lastStun) >= stunEvery {
			lastStun = now
			e.probeSTUN()
		}
		for _, p := range punches {
			e.sendPunch(p.n, p.dst)
		}
		for _, p := range pings {
			var body [8]byte
			binary.BigEndian.PutUint64(body[:], p.seq)
			if p.dst.IsValid() {
				e.writeFrame(p.n, p.dst, proto.FramePing, body[:])
			} else {
				e.writeFrameRelay(p.n, p.id, proto.FramePing, body[:])
			}
		}
		if gossipDue {
			lastGossip = now
			for _, g := range gossips {
				payload := encodeAddr(g.reflex, cands)
				if g.dst.IsValid() {
					e.writeFrame(g.n, g.dst, proto.FrameAddr, payload)
				} else {
					e.writeFrameRelay(g.n, g.id, proto.FrameAddr, payload)
				}
				// Имя шлём тем же тактом: пир, узнанный без сигналки, иначе так и
				// останется безымянным, а переименование узла не доедет никогда.
				if selfName != "" {
					if g.dst.IsValid() {
						e.writeFrame(g.n, g.dst, proto.FrameHello, []byte(selfName))
					} else {
						e.writeFrameRelay(g.n, g.id, proto.FrameHello, []byte(selfName))
					}
				}
			}
		}
		if pexDue {
			// Сбрасываем таймер тика ДАЖЕ если слать было некому/нечего — иначе
			// pexDue остаётся истинным на каждом тике punchInterval, пока не
			// появится хоть один подтверждённый пир, которому есть что разослать.
			lastPex = now
			for _, p := range pexes {
				if p.dst.IsValid() {
					e.writeFrame(p.n, p.dst, proto.FramePeers, p.payload)
				} else {
					e.writeFrameRelay(p.n, p.id, proto.FramePeers, p.payload)
				}
			}
		}
	}
}

// --- снимок состояния для UI ------------------------------------------------

// PeerView — состояние одного пира для отображения в интерфейсе.
type PeerView struct {
	Name       string  `json:"name"`
	VirtualIP  string  `json:"vip"`
	Status     string  `json:"status"`     // "direct" | "relay" | "connecting"
	Endpoint   string  `json:"endpoint"`   // подтверждённый прямой адрес, если есть
	LastSeenMs int64   `json:"lastSeenMs"` // мс с последнего пакета; -1 если не было
	RttMs      float64 `json:"rttMs"`      // задержка по рабочему пути; -1 если не измерена
	// Signals — через какие сигналки виден этот пир (по порядку signalURLs).
	// Заполняется в app.Session.State, движок оставляет nil.
	Signals []bool `json:"signals"`
	BytesRx uint64 `json:"bytesRx"` // всего принято байт данных от пира
	BytesTx uint64 `json:"bytesTx"` // всего отправлено байт данных пиру
}

// PeerViews возвращает отсортированный снимок таблицы пиров сети tag (для панели).
func (e *Engine) PeerViews(tag [relayTagLen]byte) []PeerView {
	e.mu.RLock()
	defer e.mu.RUnlock()

	n := e.nets[tag]
	if n == nil {
		return nil
	}
	now := time.Now()
	out := make([]PeerView, 0, len(n.peers))
	for _, ps := range n.peers {
		v := PeerView{Name: ps.name, VirtualIP: ps.virtualIP.String(), LastSeenMs: -1, RttMs: -1,
			BytesRx: ps.bytesRx.Load(), BytesTx: ps.bytesTx.Load()}
		switch {
		case ps.active.IsValid() && now.Sub(ps.lastRecv) < peerTimeout:
			v.Status = "direct"
			v.Endpoint = ps.active.String()
			v.LastSeenMs = now.Sub(ps.lastRecv).Milliseconds()
		case !ps.lastRelayRecv.IsZero() && now.Sub(ps.lastRelayRecv) < peerTimeout:
			// Прямо не пробились, но через ретранслятор пир отвечает.
			v.Status = "relay"
			v.LastSeenMs = now.Sub(ps.lastRelayRecv).Milliseconds()
		default:
			v.Status = "connecting"
		}
		if v.Status != "connecting" {
			// Признак «измерено» — rttAt, а НЕ ps.rtt > 0: в локалке (и на
			// localhost) задержка меньше зернистости монотонных часов Windows,
			// и честный замер выходит ровно нулевым. Проверка на >0 прятала бы
			// как раз самые быстрые соединения.
			// Протухший замер не показываем: лучше «—», чем цифра из прошлого.
			if !ps.rttAt.IsZero() && now.Sub(ps.rttAt) < rttStale {
				v.RttMs = float64(ps.rtt.Microseconds()) / 1000
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VirtualIP < out[j].VirtualIP })
	return out
}

// SettledForPolling — можно ли реже ходить на сигналку. true, когда никто из
// пиров сейчас не «ищется»: либо все на свежем прямом пути (пакет был не позже
// staleProbe назад), либо пиров нет вовсе. Порог именно staleProbe, а не
// peerTimeout: подозрительно замолчавший пир (сменился адрес) должен вернуть нас
// в быстрый темп сразу, чтобы переоткрыться через сигналку, а не через минуту.
func (e *Engine) SettledForPolling() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	now := time.Now()
	for _, nw := range e.nets {
		for _, ps := range nw.peers {
			if !ps.active.IsValid() || now.Sub(ps.lastRecv) > staleProbe {
				return false
			}
		}
	}
	return true
}

// --- отправка ---------------------------------------------------------------

// sendFrame отправляет кадр пиру лучшим доступным путём: сначала прямой, иначе
// через ретранслятор.
func (e *Engine) sendFrame(ps *peerState, typ byte, payload []byte) {
	e.mu.RLock()
	now := time.Now()
	dst := ps.directAddr(now)
	n := ps.net
	relayOK := e.relay.IsValid() && n.useRelay && ps.usableRelay(now)
	id := ps.id
	e.mu.RUnlock()

	switch {
	case dst.IsValid():
		e.writeFrame(n, dst, typ, payload)
	case relayOK:
		e.writeFrameRelay(n, id, typ, payload)
	default:
		return // путь ещё не найден — данные дропаем, как и раньше
	}
	if typ == proto.FrameData {
		ps.bytesTx.Add(uint64(len(payload)))
	}
}

// sendToAll рассылает кадр всем пирам ВСЕХ сетей (эмуляция широковещалки). Пакет
// из TUN сети не несёт, поэтому broadcast уходит во все сети — каждый пир получает
// его запечатанным ключом СВОЕЙ сети.
func (e *Engine) sendToAll(typ byte, payload []byte) {
	e.mu.RLock()
	type job struct {
		n      *network
		id     proto.PeerID
		direct netip.AddrPort
		ps     *peerState
	}
	now := time.Now()
	var jobs []job
	for _, nw := range e.nets {
		for _, ps := range nw.peers {
			switch {
			case ps.directAddr(now).IsValid():
				jobs = append(jobs, job{n: nw, direct: ps.active, ps: ps})
			case e.relay.IsValid() && nw.useRelay && ps.usableRelay(now):
				jobs = append(jobs, job{n: nw, id: ps.id, ps: ps})
			}
		}
	}
	e.mu.RUnlock()

	for _, j := range jobs {
		if j.direct.IsValid() {
			e.writeFrame(j.n, j.direct, typ, payload)
		} else {
			e.writeFrameRelay(j.n, j.id, typ, payload)
		}
		if typ == proto.FrameData || typ == proto.FrameBroadcast {
			j.ps.bytesTx.Add(uint64(len(payload)))
		}
	}
}

// directAddr — прямой endpoint пира, ЕСЛИ он ещё живой. Вызывать под локом.
//
// Сверка свежести (lastRecv) обязательна: без неё умерший прямой путь (NAT
// перевесил порт при переподключении — у мобильных операторов обычное дело)
// оставляет ps.active выставленным на мёртвый адрес, и sendFrame продолжает лить
// туда данные, ни разу не уйдя на relay. Статус в панели при этом честно
// показывает relay (он сверяет lastRecv) — и получается разъезд: «в сети», а
// трафик в чёрную дыру. Здесь та же проверка, что в PeerViews и maintenance.
func (ps *peerState) directAddr(now time.Time) netip.AddrPort {
	if ps.active.IsValid() && now.Sub(ps.lastRecv) < peerTimeout {
		return ps.active
	}
	return netip.AddrPort{}
}

// lastHeard — когда пир в последний раз подал признаки жизни любым путём; если
// не подавал вовсе, отсчитываем от знакомства. Вызывать под локом.
func (ps *peerState) lastHeard() time.Time {
	last := ps.lastRecv
	if ps.lastRelayRecv.After(last) {
		last = ps.lastRelayRecv
	}
	if last.IsZero() {
		return ps.firstSeen
	}
	return last
}

// usableRelay — стоит ли слать пиру через ретранслятор. Вызывать под локом.
//
// Ждём relayGrace от знакомства: прямой путь лучше, и сдаваться раньше времени
// незачем. Дальше шлём, даже если ответа через relay ещё не было — иначе никто
// не сделает первый шаг и путь не заработает никогда.
func (ps *peerState) usableRelay(now time.Time) bool {
	return !ps.firstSeen.IsZero() && now.Sub(ps.firstSeen) >= relayGrace
}

func (e *Engine) sendPunch(n *network, dst netip.AddrPort) {
	e.writeFrame(n, dst, proto.FramePunch, nil)
}

// writeFrame собирает кадр [тип|selfID|payload], шифрует и шлёт по UDP напрямую.
//
// Ошибку записи НЕ логируем сознательно. Пробитие NAT устроено так, что мы
// заведомо долбим недостижимые кандидаты (чужие локалки, 169.254.x, адаптеры
// VPN) — неудачная запись тут норма, а не сбой. Логирование каждой такой
// попытки забивало кольцевой буфер диагностики: у пира с парой непробитых
// соседей все 200 строк оказывались этим мусором, и до настоящих сообщений
// дело не доходило. Проблемы с путём и так видны по статусу пира.
func (e *Engine) writeFrame(n *network, dst netip.AddrPort, typ byte, payload []byte) {
	sealed, err := e.seal(n, typ, payload)
	if err != nil {
		return
	}
	// Адрес отдаём как есть, unmapped: отображение в v4-in-v6 для AF_INET6-сокета
	// делает стандартная библиотека.
	e.conn.WriteToUDPAddrPort(sealed, dst)
}

// writeFrameRelay шлёт тот же кадр через ретранслятор, завернув его в
// [0x02][тег][peerID адресата]. Внутри — обычный запечатанный кадр: ретранслятор
// его не расшифрует, ключа сети у него нет.
func (e *Engine) writeFrameRelay(n *network, dstID proto.PeerID, typ byte, payload []byte) {
	relay := e.relayAddr()
	if !relay.IsValid() {
		return
	}
	sealed, err := e.seal(n, typ, payload)
	if err != nil {
		return
	}
	pkt := make([]byte, 0, 1+relayTagLen+len(dstID)+len(sealed))
	pkt = append(pkt, relayData)
	pkt = append(pkt, n.tag[:]...)
	pkt = append(pkt, dstID[:]...)
	pkt = append(pkt, sealed...)

	// Молчим по той же причине, что и writeFrame: ошибка записи тут ничего не
	// диагностирует, а буфер логов забивает.
	e.conn.WriteToUDPAddrPort(pkt, relay)
}

// seal собирает и шифрует кадр [тип|selfID|счётчик|payload] ключом сети n.
func (e *Engine) seal(n *network, typ byte, payload []byte) ([]byte, error) {
	frame := make([]byte, frameHeader+len(payload))
	frame[0] = typ
	copy(frame[1:frameCounterOff], e.selfID[:])
	binary.BigEndian.PutUint64(frame[frameCounterOff:frameHeader], n.sendCtr.Add(1))
	copy(frame[frameHeader:], payload)
	return n.sealer.Seal(frame)
}

// acceptCounter — anti-replay: принять ли кадр со счётчиком c от этого пира.
// Обновляет скользящее окно. Вызывать под e.mu. Счётчики отправителя монотонно
// растут (network.sendCtr), поэтому повтор или заметно устаревший кадр отбрасываем.
func (ps *peerState) acceptCounter(c uint64) bool {
	const window = 64
	if c > ps.recvMax {
		if shift := c - ps.recvMax; shift >= window {
			ps.recvBits = 0
		} else {
			ps.recvBits <<= shift
		}
		ps.recvBits |= 1 // бит 0 = сам recvMax
		ps.recvMax = c
		return true
	}
	offset := ps.recvMax - c
	if offset >= window {
		return false // за окном — слишком старый
	}
	mask := uint64(1) << offset
	if ps.recvBits&mask != 0 {
		return false // повтор
	}
	ps.recvBits |= mask
	return true
}

// sendRelayBind напоминает ретранслятору наш адрес в сети n, чтобы нам было куда
// слать. Тег — сети n; ретранслятор ведёт таблицу по паре (тег, peerID).
func (e *Engine) sendRelayBind(n *network) {
	relay := e.relayAddr()
	if !relay.IsValid() {
		return
	}
	pkt := make([]byte, 0, 1+relayTagLen+len(e.selfID))
	pkt = append(pkt, relayBind)
	pkt = append(pkt, n.tag[:]...)
	pkt = append(pkt, e.selfID[:]...)
	e.conn.WriteToUDPAddrPort(pkt, relay)
}

// sameAddr больше не нужен: netip.AddrPort сравнивается оператором ==, а обе
// стороны нормализованы Unmap на чтении из сокета.

// --- обмен адресами (FrameAddr) ---------------------------------------------

// handleAddr обрабатывает FrameAddr от пира: (а) reflex — наш внешний адрес,
// каким его видит пир: запоминаем и, если сменился, будим перерегистрацию; (б)
// кандидаты пира — доливаем в его список, чтобы перепробиться на новый адрес.
func (e *Engine) handleAddr(ps *peerState, reflex string, cands []string) {
	e.mu.Lock()
	// reflex приходит от пира — валидируем формат (как relay/STUN пути), иначе
	// мусорный/чужой адрес ушёл бы в наш собственный анонс на сигналку с приоритетом.
	changed := reflex != "" && validEndpoint(reflex) && reflex != e.selfReflex
	if changed {
		e.selfReflex = reflex
	}
	if len(cands) > 0 {
		mergeCandidates(ps, cands)
	}
	ch := e.reflexCh
	e.mu.Unlock()

	if changed && ch != nil {
		select {
		case ch <- struct{}{}:
		default: // сигнал уже висит — одного достаточно
		}
	}
}

// mergeCandidates доливает присланные кандидаты в endpoints пира без дублей и с
// потолком maxCandidates: чужой госсип не должен вытеснить рабочие адреса.
// Вызывать под e.mu.Lock.
// mergeEndpoints объединяет свежий список кандидатов от сигналки с уже известными
// (в т.ч. добытыми P2P-гостипом), без дублей и не длиннее maxCandidates. Свежие идут
// первыми. Пустой fresh НЕ стирает существующие — сохраняем что было.
func mergeEndpoints(fresh, existing []netip.AddrPort) []netip.AddrPort {
	if len(fresh) == 0 {
		return existing
	}
	out := make([]netip.AddrPort, 0, maxCandidates)
	seen := make(map[string]bool, maxCandidates)
	add := func(a netip.AddrPort) {
		if !a.IsValid() || len(out) >= maxCandidates {
			return
		}
		k := a.String()
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, a)
	}
	for _, a := range fresh {
		add(a)
	}
	for _, a := range existing {
		add(a)
	}
	return out
}

func mergeCandidates(ps *peerState, cands []string) {
	have := make(map[string]bool, len(ps.endpoints))
	for _, a := range ps.endpoints {
		have[a.String()] = true
	}
	for _, c := range cands {
		if len(ps.endpoints) >= maxCandidates {
			break
		}
		if have[c] {
			continue
		}
		// Тоже парсер, а не резолвер: кандидат приходит от пира по сети, ходить
		// из-за него в DNS под e.mu нельзя (см. AddProbes).
		a, err := netip.ParseAddrPort(c)
		if err != nil {
			continue
		}
		// Кандидат прислан чужим пиром кадром FrameAddr — нормализуем mapped-форму
		// (см. AddProbes), иначе она не схлопнётся в дедупе с unmapped-адресом.
		a = netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
		if have[a.String()] {
			continue
		}
		ps.endpoints = append(ps.endpoints, a)
		have[a.String()] = true
	}
}

// encodeAddr / decodeAddr — тело FrameAddr: reflex-адрес получателя и список наших
// кандидатов. Формат: [len(1)][reflex] [cnt(1)] cnt×([len(1)][cand]).
// Строки — "ip:port", заведомо короче 256 байт, поэтому длина в один байт.
func encodeAddr(reflex string, cands []string) []byte {
	b := appendStr(make([]byte, 0, 32), reflex)
	if len(cands) > 255 {
		cands = cands[:255]
	}
	b = append(b, byte(len(cands)))
	for _, c := range cands {
		b = appendStr(b, c)
	}
	return b
}

func appendStr(b []byte, s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	b = append(b, byte(len(s)))
	return append(b, s...)
}

func decodeAddr(p []byte) (reflex string, cands []string) {
	reflex, p, ok := takeStr(p)
	if !ok || len(p) < 1 {
		return reflex, nil
	}
	n := int(p[0])
	p = p[1:]
	for i := 0; i < n; i++ {
		var s string
		if s, p, ok = takeStr(p); !ok {
			break
		}
		if s != "" {
			cands = append(cands, s)
		}
	}
	return reflex, cands
}

func takeStr(p []byte) (string, []byte, bool) {
	if len(p) < 1 {
		return "", p, false
	}
	n := int(p[0])
	p = p[1:]
	if len(p) < n {
		return "", p, false
	}
	return string(p[:n]), p[n:], true
}

// --- PEX: обмен адресами пиров (FramePeers) ---------------------------------

// encodePeers/decodePeers — тело FramePeers: [count:1], затем count раз
// [family:1][addr:4|16][port:2 big-endian]. Только адреса, без PeerID — кто
// окажется за адресом, выяснится при расшифровке ключом сети (см. FramePeers).
func encodePeers(addrs []netip.AddrPort) []byte {
	if len(addrs) > maxPexEntries {
		addrs = addrs[:maxPexEntries]
	}
	out := make([]byte, 0, 1+len(addrs)*19)
	out = append(out, byte(len(addrs)))
	for _, ap := range addrs {
		// Без Unmap: сюда приходят только внутренние адреса движка (ps.active),
		// а инвариант пакета гарантирует, что mapped-формы среди них нет —
		// нормализация уже сделана на всех входах (см. netToTun/AddProbes).
		a := ap.Addr()
		if a.Is4() {
			b := a.As4()
			out = append(out, 4)
			out = append(out, b[:]...)
		} else {
			b := a.As16()
			out = append(out, 6)
			out = append(out, b[:]...)
		}
		out = append(out, byte(ap.Port()>>8), byte(ap.Port()))
	}
	return out
}

func decodePeers(p []byte) []netip.AddrPort {
	if len(p) < 1 {
		return nil
	}
	n := int(p[0])
	if n > maxPexEntries {
		n = maxPexEntries
	}
	p = p[1:]
	var out []netip.AddrPort
	for i := 0; i < n; i++ {
		if len(p) < 1 {
			break
		}
		var size int
		switch p[0] {
		case 4:
			size = 4
		case 6:
			size = 16
		default:
			// Неизвестное семейство: длину записи посчитать нельзя, поэтому
			// дальше разбирать нечего — выходим, но уже собранное отдаём.
			return out
		}
		if len(p) < 1+size+2 {
			break
		}
		addr, ok := netip.AddrFromSlice(p[1 : 1+size])
		if ok {
			port := uint16(p[1+size])<<8 | uint16(p[1+size+1])
			out = append(out, netip.AddrPortFrom(addr.Unmap(), port))
		}
		p = p[1+size+2:]
	}
	return out
}

// --- разбор IPv4 ------------------------------------------------------------

// ipv4Dst достаёт адрес назначения из IPv4-пакета.
func ipv4Dst(pkt []byte) (netip.Addr, bool) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{pkt[16], pkt[17], pkt[18], pkt[19]}), true
}

// isBroadcastLike — истина для limited broadcast, broadcast нашей /8 и мультикаста.
func isBroadcastLike(a netip.Addr) bool {
	b := a.As4()
	if b == [4]byte{255, 255, 255, 255} {
		return true
	}
	if b[0] == 25 && b[1] == 255 && b[2] == 255 && b[3] == 255 { // 25.255.255.255
		return true
	}
	return b[0] >= 224 && b[0] <= 239 // 224.0.0.0/4 multicast
}
