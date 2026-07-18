// Package peer — движок mesh-сети: шифрованный UDP-транспорт между пирами,
// маршрутизация IP-пакетов, пробитие NAT и эмуляция широковещания для LAN-игр.
package peer

import (
	"encoding/binary"
	"net"
	"net/netip"
	"sort"
	"sync"
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

// Раскладка расшифрованного кадра: [0]=тип, [1:17]=PeerID отправителя, [17:]=нагрузка.
const (
	frameHeader = 1 + 16
	maxPacket   = 1500 // MTU виртуальной сети
	maxFrame    = frameHeader + maxPacket
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

	// Живой переопрос STUN на боевом сокете — чтобы внешний адрес не замерзал на
	// стартовом значении: NAT со временем перевешивает порт, и старый адрес ломает
	// прямой путь до самого реконнекта. Часто, пока путь деградировал (есть relay/
	// suspect/непробитый пир — тогда важно быстро переоткрыть свой адрес), редко,
	// когда всё стабильно (ловить тихие ребинды).
	stunRecheckFast  = 20 * time.Second
	stunRecheckSlow  = 2 * time.Minute
	stunProbeTimeout = 5 * time.Second // ждём ответ; позже — чистим повисший txID
)

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
}

type peerState struct {
	net       *network // сеть, которой принадлежит пир (её ключом шифруем трафик к нему)
	id        proto.PeerID
	name      string
	virtualIP netip.Addr
	endpoints []*net.UDPAddr // кандидаты (STUN + локальные)
	active    *net.UDPAddr   // подтверждён по входящему пакету — ТОЛЬКО прямой путь
	lastRecv  time.Time      // когда пришёл прямой пакет
	firstSeen time.Time      // когда узнали о пире — от него отсчитываем relayGrace

	// Ретранслятор. lastRelayRecv отдельно от lastRecv: пакет через relay НЕ
	// подтверждает прямой endpoint, иначе мы бы считали дырку пробитой и слали
	// данные в никуда.
	lastRelayRecv time.Time

	// absentSince — когда пир пропал из ответа сигналки; ноль = он там есть.
	// Нужен, чтобы не сносить пира по одной осечке сигналки, см. SyncPeers.
	absentSince time.Time

	// Замер задержки. pingAt — момент отправки ping с номером pingSeq; обнуляется
	// при получении ответа. Время только локальное (монотонное), часы пиров не
	// участвуют.
	pingSeq  uint64
	pingAt   time.Time
	pingSent time.Time
	rtt      time.Duration // 0 = ещё не измерен
	rttAt    time.Time     // когда измерен — чтобы не показывать протухший
}

// Engine связывает TUN и UDP-сокет, ведёт таблицы пиров ПО СЕТЯМ и гоняет между
// ними трафик. Сокет, адаптер и внешний адрес — общие; сети (ключ+тег+пиры)
// добавляются/убираются на ходу через AddNetwork/RemoveNetwork.
type Engine struct {
	conn   *net.UDPConn
	tun    TUN
	selfID proto.PeerID
	selfIP netip.Addr

	// Ретранслятор общий (адрес один); тег у каждой сети свой, см. network.
	relay *net.UDPAddr

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

// AddNetwork подключает сеть (ключ+тег+имя) на ходу. Идемпотентно: повторный вызов
// с тем же тегом обновляет ключ/имя, таблицу пиров сохраняет. Потокобезопасно.
func (e *Engine) AddNetwork(tag [relayTagLen]byte, sealer *crypto.Sealer, name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n := e.nets[tag]; n != nil {
		n.sealer = sealer
		n.name = name
		return
	}
	e.nets[tag] = &network{
		tag:    tag,
		sealer: sealer,
		name:   name,
		peers:  make(map[proto.PeerID]*peerState),
		byIP:   make(map[netip.Addr]*peerState),
	}
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

// UseRelay задаёт адрес ретранслятора — общий на все сети (он ведёт таблицу по
// паре (тег, peerID), а тег у каждой сети свой). Расшифровать он ничего не может.
func (e *Engine) UseRelay(addr *net.UDPAddr) {
	e.mu.Lock()
	e.relay = addr
	e.mu.Unlock()
}

// relayAddr отдаёт адрес ретранслятора (nil, если не задан).
func (e *Engine) relayAddr() *net.UDPAddr {
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
		vip, err := netip.ParseAddr(info.VirtualIP)
		if err != nil {
			continue
		}
		seen[id] = true

		eps := make([]*net.UDPAddr, 0, len(info.Endpoints))
		for _, s := range info.Endpoints {
			if a, err := net.ResolveUDPAddr("udp4", s); err == nil {
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
		ps.absentSince = time.Time{} // снова в списке
		ps.name = info.Name
		ps.virtualIP = vip
		ps.endpoints = eps
		n.byIP[vip] = ps
	}

	for id, ps := range n.peers {
		if seen[id] {
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
		n, src, err := e.conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}

		raw := buf[:n]
		viaRelay := false
		if relay := e.relayAddr(); relay != nil && sameAddr(src, relay) {
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

		var senderID proto.PeerID
		copy(senderID[:], plain[1:frameHeader])
		typ := plain[0]
		payload := plain[frameHeader:]

		e.mu.Lock()
		ps := nw.peers[senderID]
		if ps != nil {
			if viaRelay {
				// Через ретранслятор: пир жив, но прямая дырка НЕ пробита.
				// Записать src в active было бы ошибкой — это адрес relay, и мы
				// бы решили, что пробились, перестав долбить кандидаты.
				ps.lastRelayRecv = time.Now()
			} else {
				// Прямой валидный пакет — вот он, итог пробития NAT.
				ps.active = src
				ps.lastRecv = time.Now()
			}
		}
		e.mu.Unlock()
		if ps == nil {
			continue // пир не из таблицы (сигналка ещё не отдала) — игнор
		}

		switch typ {
		case proto.FrameData, proto.FrameBroadcast:
			if len(payload) > 0 {
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
		}
	}
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
		dst *net.UDPAddr // прямой адрес; nil = через ретранслятор
		id  proto.PeerID
		seq uint64
	}
	type punchJob struct {
		n   *network
		dst *net.UDPAddr
	}
	type gossipJob struct {
		n      *network
		dst    *net.UDPAddr // прямой адрес; nil = через ретранслятор
		id     proto.PeerID
		reflex string // адрес пира, каким мы его видим ("" через relay)
	}

	var lastBind, lastGossip, lastStun time.Time
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
		gossipDue := now.Sub(lastGossip) >= addrGossipTick

		e.mu.Lock()
		hasRelay := e.relay != nil
		hasStun := len(e.stunServers) > 0
		degraded := false // хоть один пир не на свежем прямом пути → чаще переспрашиваем свой адрес
		cands := append([]string(nil), e.selfCands...)
		netsList := make([]*network, 0, len(e.nets))
		for _, nw := range e.nets {
			netsList = append(netsList, nw)
			for _, ps := range nw.peers {
				confirmed := ps.active != nil && now.Sub(ps.lastRecv) < peerTimeout
				relayPath := !confirmed && hasRelay && ps.usableRelay(now)
				// suspect — подтверждённый пир, но давно не слышно: возможно, у него
				// сменился адрес. Начинаем пробивать кандидаты заново, не дожидаясь
				// peerTimeout, — иначе без ретранслятора это минута отвала.
				suspect := ps.active != nil && now.Sub(ps.lastRecv) > staleProbe
				if !confirmed || suspect || relayPath {
					degraded = true // путь к этому пиру не идеален — пора освежить свой адрес
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
				if gossipDue && len(cands) > 0 && (confirmed || relayPath) {
					// Рассылаем свои кандидаты (и reflex-адрес пира) по рабочему пути.
					if confirmed {
						gossips = append(gossips, gossipJob{n: nw, dst: ps.active, reflex: ps.active.String()})
					} else {
						gossips = append(gossips, gossipJob{n: nw, id: ps.id}) // через relay reflex не знаем
					}
				}
			}
		}
		e.mu.Unlock()

		if hasRelay && now.Sub(lastBind) >= relayBindTick {
			lastBind = now
			for _, nw := range netsList { // напоминаем о себе релею в каждой сети (свой тег)
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
			if p.dst != nil {
				e.writeFrame(p.n, p.dst, proto.FramePing, body[:])
			} else {
				e.writeFrameRelay(p.n, p.id, proto.FramePing, body[:])
			}
		}
		if gossipDue {
			lastGossip = now
			for _, g := range gossips {
				payload := encodeAddr(g.reflex, cands)
				if g.dst != nil {
					e.writeFrame(g.n, g.dst, proto.FrameAddr, payload)
				} else {
					e.writeFrameRelay(g.n, g.id, proto.FrameAddr, payload)
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
		v := PeerView{Name: ps.name, VirtualIP: ps.virtualIP.String(), LastSeenMs: -1, RttMs: -1}
		switch {
		case ps.active != nil && now.Sub(ps.lastRecv) < peerTimeout:
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
			if ps.active == nil || now.Sub(ps.lastRecv) > staleProbe {
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
	relayOK := e.relay != nil && ps.usableRelay(now)
	id := ps.id
	n := ps.net
	e.mu.RUnlock()

	switch {
	case dst != nil:
		e.writeFrame(n, dst, typ, payload)
	case relayOK:
		e.writeFrameRelay(n, id, typ, payload)
	}
	// Иначе путь ещё не найден — данные дропаем, как и раньше.
}

// sendToAll рассылает кадр всем пирам ВСЕХ сетей (эмуляция широковещалки). Пакет
// из TUN сети не несёт, поэтому broadcast уходит во все сети — каждый пир получает
// его запечатанным ключом СВОЕЙ сети.
func (e *Engine) sendToAll(typ byte, payload []byte) {
	e.mu.RLock()
	type job struct {
		n      *network
		id     proto.PeerID
		direct *net.UDPAddr
	}
	now := time.Now()
	var jobs []job
	for _, nw := range e.nets {
		for _, ps := range nw.peers {
			switch {
			case ps.directAddr(now) != nil:
				jobs = append(jobs, job{n: nw, direct: ps.active})
			case e.relay != nil && ps.usableRelay(now):
				jobs = append(jobs, job{n: nw, id: ps.id})
			}
		}
	}
	e.mu.RUnlock()

	for _, j := range jobs {
		if j.direct != nil {
			e.writeFrame(j.n, j.direct, typ, payload)
		} else {
			e.writeFrameRelay(j.n, j.id, typ, payload)
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
func (ps *peerState) directAddr(now time.Time) *net.UDPAddr {
	if ps.active != nil && now.Sub(ps.lastRecv) < peerTimeout {
		return ps.active
	}
	return nil
}

// usableRelay — стоит ли слать пиру через ретранслятор. Вызывать под локом.
//
// Ждём relayGrace от знакомства: прямой путь лучше, и сдаваться раньше времени
// незачем. Дальше шлём, даже если ответа через relay ещё не было — иначе никто
// не сделает первый шаг и путь не заработает никогда.
func (ps *peerState) usableRelay(now time.Time) bool {
	return !ps.firstSeen.IsZero() && now.Sub(ps.firstSeen) >= relayGrace
}

func (e *Engine) sendPunch(n *network, dst *net.UDPAddr) {
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
func (e *Engine) writeFrame(n *network, dst *net.UDPAddr, typ byte, payload []byte) {
	sealed, err := e.seal(n, typ, payload)
	if err != nil {
		return
	}
	e.conn.WriteToUDP(sealed, dst)
}

// writeFrameRelay шлёт тот же кадр через ретранслятор, завернув его в
// [0x02][тег][peerID адресата]. Внутри — обычный запечатанный кадр: ретранслятор
// его не расшифрует, ключа сети у него нет.
func (e *Engine) writeFrameRelay(n *network, dstID proto.PeerID, typ byte, payload []byte) {
	relay := e.relayAddr()
	if relay == nil {
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
	e.conn.WriteToUDP(pkt, relay)
}

// seal собирает и шифрует кадр [тип|selfID|payload] ключом сети n.
func (e *Engine) seal(n *network, typ byte, payload []byte) ([]byte, error) {
	frame := make([]byte, frameHeader+len(payload))
	frame[0] = typ
	copy(frame[1:frameHeader], e.selfID[:])
	copy(frame[frameHeader:], payload)
	return n.sealer.Seal(frame)
}

// sendRelayBind напоминает ретранслятору наш адрес в сети n, чтобы нам было куда
// слать. Тег — сети n; ретранслятор ведёт таблицу по паре (тег, peerID).
func (e *Engine) sendRelayBind(n *network) {
	relay := e.relayAddr()
	if relay == nil {
		return
	}
	pkt := make([]byte, 0, 1+relayTagLen+len(e.selfID))
	pkt = append(pkt, relayBind)
	pkt = append(pkt, n.tag[:]...)
	pkt = append(pkt, e.selfID[:]...)
	e.conn.WriteToUDP(pkt, relay)
}

// sameAddr сравнивает UDP-адреса по значению (указатели тут разные всегда).
func sameAddr(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.Port == b.Port && a.IP.Equal(b.IP)
}

// --- обмен адресами (FrameAddr) ---------------------------------------------

// handleAddr обрабатывает FrameAddr от пира: (а) reflex — наш внешний адрес,
// каким его видит пир: запоминаем и, если сменился, будим перерегистрацию; (б)
// кандидаты пира — доливаем в его список, чтобы перепробиться на новый адрес.
func (e *Engine) handleAddr(ps *peerState, reflex string, cands []string) {
	e.mu.Lock()
	changed := reflex != "" && reflex != e.selfReflex
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
		a, err := net.ResolveUDPAddr("udp4", c)
		if err != nil || a.IP == nil || have[a.String()] {
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
