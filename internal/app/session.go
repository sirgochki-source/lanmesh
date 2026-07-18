// Package app — переиспользуемый «сеанс» mesh-сети. Держит ОДИН узел (сокет, TUN,
// движок, внешний адрес) и НЕСКОЛЬКО сетей на нём одновременно (как Radmin): у
// каждой сети свой ключ, тег и цикл регистрации, но сокет/адаптер/STUN общие.
// Используется и headless-CLI, и GUI.
package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/logbuf"
	"github.com/sirgochki-source/lanmesh/internal/peer"
	"github.com/sirgochki-source/lanmesh/internal/proto"
	"github.com/sirgochki-source/lanmesh/internal/signal"
	"github.com/sirgochki-source/lanmesh/internal/tun"
)

// logFlushInterval — как часто сливаем накопленный лог на сигналку.
const logFlushInterval = 30 * time.Second

// Темп регистрации на сигналках. Разгонный на старте и при появлении нового
// участника (чтобы стороны быстро обменялись адресами и начали пробитие NAT),
// быстрый, пока сеть «неустаканена», и медленный, когда все пиры на свежем прямом
// пути или мы одни. См. Engine.SettledForPolling.
//
// Почему разгон важен: пробитие NAT требует, чтобы ОБЕ стороны долбили навстречу.
// Пир узнаёт эндпоинт соседа только из ответа сигналки, поэтому пока один опрашивает
// её раз в 20с, встречное пробитие может не начаться до следующего опроса — и
// «первичное подключение» тянется десятки секунд. В разгоне опрос раз в registerBurst
// секунд, но лишь burstWindow после старта/появления пира — потом темп падает, чтобы
// не молотить сигналку из-за пира, до которого прямого пути нет (только релей).
const (
	registerBurst = 2 * time.Second
	registerFast  = 20 * time.Second
	registerSlow  = 45 * time.Second
	settleWarmup  = 60 * time.Second
	burstWindow   = 25 * time.Second
)

// StateView — снимок состояния узла для интерфейса.
//
// Running означает лишь «адаптер поднят». Реальную работоспособность показывают
// SelfEndpoint и SignalError сетей. Networks несёт ВСЕ сети; поля Network/Signals/
// Peers/SignalError продублированы для первой сети — ради совместимости со старой
// одно-сетевой панелью, пока она не переехала на список Networks.
type StateView struct {
	Running      bool          `json:"running"`
	SelfName     string        `json:"selfName"`
	SelfIP       string        `json:"selfIP"`
	SelfEndpoint string        `json:"selfEndpoint"` // внешний адрес; "" = не определён
	StunVia      string        `json:"stunVia"`
	UptimeSec    int64         `json:"uptimeSec"`
	Error        string        `json:"error"`
	Networks     []NetworkView `json:"networks"`

	// Зеркало первой сети (устаревшее, для старой панели).
	Network     string          `json:"network"`
	SignalError string          `json:"signalError"`
	Signals     []SignalView    `json:"signals"`
	Peers       []peer.PeerView `json:"peers"`
}

// NetworkView — состояние одной сети: имя, тег, доступность сигналок и участники.
type NetworkView struct {
	Name        string          `json:"name"`
	Tag         string          `json:"tag"`
	SignalError string          `json:"signalError"`
	Signals     []SignalView    `json:"signals"`
	Peers       []peer.PeerView `json:"peers"`
}

// SignalView — состояние одной сигналки: хост и ответила ли она в последнем раунде.
type SignalView struct {
	Host string `json:"host"`
	Up   bool   `json:"up"`
}

// netSession — одна сеть на общем узле: имя+пароль (для инвайта), тег и её цикл.
type netSession struct {
	name     string
	password string // для генерации инвайта; хранится только при Remember у GUI
	tag      string // hex — для сигналки/логов
	tagB     [32]byte
	stop     chan struct{} // закрывается при выходе из сети
	kick     chan struct{} // разбудить перерегистрацию (смена внешнего адреса)

	// Под mu. Доступность сигналок и ошибка — свои у каждой сети.
	signalUp    []bool
	peerSignals map[string][]bool
	signalErr   string
}

// Session — узел mesh с несколькими сетями. Потокобезопасен.
type Session struct {
	// Конфиг узла.
	signalURLs  []string
	stunServers []string
	relayAddr   string
	iface       string
	logs        *logbuf.Buffer
	logUpload   atomic.Bool

	// opMu сериализует изменения состава (AddNetwork/RemoveNetwork/Stop); mu
	// защищает чтение/запись полей. Так медленная поднятие узла (STUN, адаптер) не
	// держит mu, под которым работают горутины и State.
	opMu sync.Mutex
	mu   sync.Mutex
	// endpointMu сериализует пересчёт внешнего адреса узла: nodeEndpoints зовёт
	// КАЖДЫЙ registerLoop (по одному на сеть) со своим тактом, и без сериализации
	// параллельные вызовы писали неактуальное поверх свежего и накручивали extChurn.
	endpointMu sync.Mutex

	// Узел (общий, пока поднят).
	up           bool
	selfID       proto.PeerID
	peerID       string // selfID.String()
	selfIP       netip.Addr
	selfEndpoint string // текущий внешний адрес (обновляют registerLoop-ы)
	stunVia      string
	stunExt      string // стартовый STUN
	localPort    int
	startedAt    time.Time
	lastErr      string
	extChurn     int
	signalSeen   string // IP глазами сигналки (не подделать) — для сверки со STUN
	relaySeen    string // внешний адрес глазами релея

	engine     *peer.Engine
	conn       *net.UDPConn
	dev        *tun.Device
	nodeStop   chan struct{}   // останавливает горутины уровня узла (Run/logLoop/fanout)
	kickFanout []chan struct{} // kick-каналы всех сетей (fanout сигнала reflex)

	nets map[[32]byte]*netSession // сети по tagB
}

// NewSession создаёт узел, привязанный к списку сигналок и STUN-серверов. Пустой
// список STUN — signal.DefaultSTUNServers.
func NewSession(signalURLs []string, stunServers []string, iface string) *Session {
	if len(stunServers) == 0 {
		stunServers = signal.DefaultSTUNServers
	}
	return &Session{
		signalURLs:  signalURLs,
		stunServers: stunServers,
		iface:       iface,
		nets:        make(map[[32]byte]*netSession),
	}
}

// UseRelay задаёт ретранслятор ("host:port") — общий на все сети. Вызывать до Start.
func (s *Session) UseRelay(addr string) {
	s.mu.Lock()
	s.relayAddr = addr
	s.mu.Unlock()
}

// SetSignalURLs меняет список сигналок. Разрешено ТОЛЬКО пока узел снят: register-
// циклы берут снимок при старте и читают из горутин, подмена на ходу — гонка.
func (s *Session) SetSignalURLs(urls []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.up {
		return errors.New("нельзя менять сигналки на ходу — сначала отключись")
	}
	s.signalURLs = urls
	return nil
}

// EnableLogUpload подключает отправку диагностики на сигналку из buf.
func (s *Session) EnableLogUpload(buf *logbuf.Buffer, enabled bool) {
	s.mu.Lock()
	s.logs = buf
	s.mu.Unlock()
	s.logUpload.Store(enabled)
}

// SetLogUpload включает/выключает отправку логов на лету.
func (s *Session) SetLogUpload(enabled bool) { s.logUpload.Store(enabled) }

// Start подключает одну сеть (для CLI и обратной совместимости) — это AddNetwork.
func (s *Session) Start(network, password string) error { return s.AddNetwork(network, password) }

// AddNetwork присоединяет сеть (имя+пароль) на ходу. Поднимает узел, если он ещё
// не поднят. Повторный вызов с той же сетью — no-op. Возвращается, когда сеть
// зарегистрирована в фоне (или с ошибкой поднятия узла).
func (s *Session) AddNetwork(network, password string) error {
	if network == "" || password == "" {
		return errors.New("нужны имя сети и пароль")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	key := crypto.DeriveNetworkKey(network, password)
	tag := signal.NetworkTag(key)
	var tagB [32]byte
	if raw, err := hex.DecodeString(tag); err != nil || len(raw) != len(tagB) {
		return errors.New("не удалось вычислить тег сети")
	} else {
		copy(tagB[:], raw)
	}

	s.mu.Lock()
	_, exists := s.nets[tagB]
	up := s.up
	s.mu.Unlock()
	if exists {
		return nil // уже в этой сети
	}

	broughtUp := false
	if !up {
		if err := s.bringUpNode(); err != nil {
			return err
		}
		broughtUp = true
	}

	sealer, err := crypto.NewSealer(key)
	if err != nil {
		// Узел подняли ради этой сети, а она не встала — не оставляем висеть узел
		// (TUN-адаптер, сокет, горутины) без единой сети до явного Stop().
		if broughtUp {
			s.tearDownNode()
		}
		return err
	}
	s.engine.AddNetwork(tagB, sealer, network)

	ns := &netSession{
		name:     network,
		password: password,
		tag:      tag,
		tagB:     tagB,
		stop:     make(chan struct{}),
		kick:     make(chan struct{}, 1),
	}
	s.mu.Lock()
	s.nets[tagB] = ns
	s.rebuildFanoutLocked()
	s.mu.Unlock()

	go s.registerLoop(ns)
	log.Printf("сеть %s подключена", tagShort(tag))
	return nil
}

// RemoveNetwork выходит из сети по тегу. Если это была последняя — снимает узел.
func (s *Session) RemoveNetwork(tag [32]byte) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	ns := s.nets[tag]
	if ns == nil {
		s.mu.Unlock()
		return
	}
	delete(s.nets, tag)
	s.rebuildFanoutLocked()
	remaining := len(s.nets)
	tagHex := ns.tag
	s.mu.Unlock()

	close(ns.stop)
	if s.engine != nil {
		s.engine.RemoveNetwork(tag)
	}
	log.Printf("сеть %s отключена", tagShort(tagHex))
	if remaining == 0 {
		s.tearDownNode()
	}
}

// Stop выходит из ВСЕХ сетей и снимает узел.
func (s *Session) Stop() {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	list := make([]*netSession, 0, len(s.nets))
	for _, ns := range s.nets {
		list = append(list, ns)
	}
	s.nets = make(map[[32]byte]*netSession)
	s.kickFanout = nil
	eng := s.engine
	s.mu.Unlock()

	for _, ns := range list {
		close(ns.stop)
		if eng != nil {
			eng.RemoveNetwork(ns.tagB)
		}
	}
	s.tearDownNode()
}

// bringUpNode поднимает общий узел: сокет, STUN, адаптер, движок и его фоновые
// горутины. Вызывать под opMu, БЕЗ mu (STUN и адаптер медленные).
func (s *Session) bringUpNode() error {
	selfID, err := LoadOrCreateIdentity()
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	selfIP := proto.VirtualIP(selfID)

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return fmt.Errorf("udp listen: %w", err)
	}
	localPort := conn.LocalAddr().(*net.UDPAddr).Port

	// Внешний адрес узнаём один раз; дальше registerLoop-ы держат его свежим через
	// reflex и живой STUN, см. Engine.
	ext, via, stunErr := signal.DiscoverEndpointAny(conn, s.stunServers)
	if stunErr != nil {
		log.Printf("STUN не ответил (%v) — только локальные кандидаты, узел будет недостижим извне", stunErr)
	} else {
		log.Printf("STUN: внешний адрес %s (через %s)", ext, via)
	}

	dev, err := tun.New(s.iface, selfIP, 8)
	if err != nil {
		conn.Close()
		return err
	}

	engine := peer.NewEngine(conn, dev, selfID, selfIP)
	engine.SetSTUNServers(s.stunServers)

	s.mu.Lock()
	relayAddr := s.relayAddr
	s.mu.Unlock()
	if relayAddr != "" {
		if raddr, err := net.ResolveUDPAddr("udp4", relayAddr); err != nil {
			log.Printf("ретранслятор %s не разрешился (%v) — только прямые соединения", relayAddr, err)
		} else {
			engine.UseRelay(raddr)
			log.Printf("ретранслятор: %s (%s)", relayAddr, raddr)
		}
	}

	nodeStop := make(chan struct{})
	kick := engine.ReflexNotify() // берём ДО Run, чтобы не пропустить ранний сигнал
	hostname, _ := os.Hostname()

	s.mu.Lock()
	s.up = true
	s.selfID = selfID
	s.peerID = selfID.String()
	s.selfIP = selfIP
	s.selfEndpoint = ext
	s.stunVia = via
	s.stunExt = ext
	s.localPort = localPort
	s.startedAt = time.Now()
	s.lastErr = ""
	s.extChurn = 0
	s.engine = engine
	s.conn = conn
	s.dev = dev
	s.nodeStop = nodeStop
	s.mu.Unlock()

	go func() {
		if err := engine.Run(); err != nil {
			select {
			case <-nodeStop:
			default:
				s.mu.Lock()
				s.lastErr = err.Error()
				s.mu.Unlock()
			}
		}
	}()
	go s.reflexFanout(kick, nodeStop)
	go s.logLoop(nodeStop, hostname)

	log.Printf("узел поднят, виртуальный IP %s", selfIP)
	return nil
}

// tearDownNode снимает узел (когда сетей не осталось). Вызывать под opMu.
func (s *Session) tearDownNode() {
	s.mu.Lock()
	if !s.up {
		s.mu.Unlock()
		return
	}
	s.up = false
	nodeStop, dev, conn := s.nodeStop, s.dev, s.conn
	s.engine, s.dev, s.conn, s.nodeStop = nil, nil, nil, nil
	s.peerID, s.selfEndpoint = "", ""
	s.mu.Unlock()

	if nodeStop != nil {
		close(nodeStop)
	}
	if dev != nil {
		dev.Close()
	}
	if conn != nil {
		conn.Close()
	}
	log.Printf("узел снят")
}

// rebuildFanoutLocked пересобирает список kick-каналов из текущих сетей. Под mu.
func (s *Session) rebuildFanoutLocked() {
	s.kickFanout = s.kickFanout[:0]
	for _, ns := range s.nets {
		s.kickFanout = append(s.kickFanout, ns.kick)
	}
}

// reflexFanout разносит сигнал «внешний адрес сменился» из движка во все сети,
// чтобы каждая перерегистрировалась с новым адресом сразу.
func (s *Session) reflexFanout(engineKick <-chan struct{}, nodeStop <-chan struct{}) {
	for {
		select {
		case <-nodeStop:
			return
		case <-engineKick:
			s.mu.Lock()
			kicks := append([]chan struct{}(nil), s.kickFanout...)
			s.mu.Unlock()
			for _, k := range kicks {
				select {
				case k <- struct{}{}:
				default: // сигнал уже висит — одного достаточно
				}
			}
		}
	}
}

// State отдаёт снимок узла и всех его сетей.
func (s *Session) State() StateView {
	s.mu.Lock()
	defer s.mu.Unlock()

	hostname, _ := os.Hostname()
	st := StateView{Running: s.up, SelfName: hostname, Error: s.lastErr}
	if !s.up {
		return st
	}
	st.SelfIP = s.selfIP.String()
	st.SelfEndpoint = s.selfEndpoint
	st.StunVia = s.stunVia
	st.UptimeSec = int64(time.Since(s.startedAt).Seconds())

	for _, ns := range s.nets {
		nv := NetworkView{Name: ns.name, Tag: ns.tag, SignalError: ns.signalErr}
		nv.Signals = make([]SignalView, len(s.signalURLs))
		for i, u := range s.signalURLs {
			nv.Signals[i] = SignalView{Host: short(u), Up: i < len(ns.signalUp) && ns.signalUp[i]}
		}
		if s.engine != nil {
			nv.Peers = s.engine.PeerViews(ns.tagB)
			for i := range nv.Peers {
				nv.Peers[i].Signals = ns.peerSignals[nv.Peers[i].VirtualIP]
			}
		}
		st.Networks = append(st.Networks, nv)
	}
	sort.Slice(st.Networks, func(i, j int) bool { return st.Networks[i].Name < st.Networks[j].Name })

	// Зеркало первой сети — для старой панели.
	if len(st.Networks) > 0 {
		p := st.Networks[0]
		st.Network, st.SignalError, st.Signals, st.Peers = p.Name, p.SignalError, p.Signals, p.Peers
	}
	return st
}

// SendDiagnostics немедленно кладёт в лог свежий снимок и заливает накопленную
// диагностику во ВСЕ сигналки под тегами всех сетей. Возвращает тег первой сети.
func (s *Session) SendDiagnostics(ctx context.Context) (string, error) {
	s.mu.Lock()
	up, peerID, buf := s.up, s.peerID, s.logs
	urls := append([]string(nil), s.signalURLs...)
	tags := make([]string, 0, len(s.nets))
	for _, ns := range s.nets {
		tags = append(tags, ns.tag)
	}
	s.mu.Unlock()

	if !up || len(tags) == 0 {
		return "", errors.New("нет ни одной подключённой сети")
	}
	sort.Strings(tags)
	first := tags[0]
	if buf == nil {
		return first, errors.New("буфер логов недоступен")
	}

	for _, line := range s.diagnosticSnapshot() {
		log.Print(line)
	}

	lines := buf.Drain()
	if len(lines) == 0 {
		return first, nil // уже слилось штатным logLoop
	}

	hostname, _ := os.Hostname()
	anyOK := false
	var wg sync.WaitGroup
	var okMu sync.Mutex
	for _, tag := range tags {
		req := proto.LogRequest{NetworkTag: tag, PeerID: peerID, Name: hostname, Lines: lines}
		for _, u := range urls {
			wg.Add(1)
			go func(u string, req proto.LogRequest) {
				defer wg.Done()
				cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if signal.NewClient(u).SendLogs(cctx, req) == nil {
					okMu.Lock()
					anyOK = true
					okMu.Unlock()
				}
			}(u, req)
		}
	}
	wg.Wait()

	if !anyOK {
		buf.PutBack(lines)
		return first, errors.New("ни одна сигналка не приняла диагностику")
	}
	return first, nil
}

// diagnosticSnapshot — человекочитаемый снимок состояния узла для отправки в лог.
func (s *Session) diagnosticSnapshot() []string {
	st := s.State()
	env := s.Diagnose()
	out := []string{"=== ДИАГНОСТИКА ==="}

	out = append(out, fmt.Sprintf("NAT: %s (ответило STUN %d/%d), внешний IPv4: %s",
		env.NATType, env.StunAnswered, env.StunTotal, orDash(env.External)))
	if env.SignalSeenIP != "" {
		out = append(out, fmt.Sprintf("сигналка видит нас с IP: %s (сверка со STUN: %s)", env.SignalSeenIP, env.StunVsSignal))
	}
	if env.RelaySeenEP != "" {
		out = append(out, fmt.Sprintf("релей видит нас как: %s (сверка со STUN: %s)", env.RelaySeenEP, env.StunVsRelay))
	}
	egress := "выход в интернет: " + orDash(env.EgressIface)
	if env.EgressIP != "" {
		egress += " (" + env.EgressIP + ")"
	}
	if env.EgressIsVPN {
		egress += " ← ЧЕРЕЗ VPN/ТУННЕЛЬ"
	}
	out = append(out, egress)
	if len(env.VPNAdapters) > 0 {
		out = append(out, "активные туннели: "+strings.Join(env.VPNAdapters, ", "))
	}
	for _, wn := range env.Warnings {
		out = append(out, "⚠ "+wn)
	}

	if st.SelfEndpoint == "" {
		out = append(out, "внешний адрес(узел): НЕ ОПРЕДЕЛЁН (STUN молчит) — извне не пробить")
	} else {
		out = append(out, fmt.Sprintf("внешний адрес(узел): %s (via %s), смен адреса: %d", st.SelfEndpoint, st.StunVia, env.EndpointFlaps))
	}

	for _, nv := range st.Networks {
		sig := make([]string, 0, len(nv.Signals))
		for _, sv := range nv.Signals {
			mark := "up"
			if !sv.Up {
				mark = "DOWN"
			}
			sig = append(sig, sv.Host+"="+mark)
		}
		out = append(out, fmt.Sprintf("--- сеть %q: сигналки %s", nv.Name, strings.Join(sig, ", ")))
		if nv.SignalError != "" {
			out = append(out, "  ошибка сигналки: "+nv.SignalError)
		}
		if len(nv.Peers) == 0 {
			out = append(out, "  пиры: пусто — никого не видно")
		}
		for _, p := range nv.Peers {
			out = append(out, fmt.Sprintf("  пир %s (%s): %s rtt=%.0fмс seen=%dмс сигналки=%v",
				p.Name, p.VirtualIP, p.Status, p.RttMs, p.LastSeenMs, p.Signals))
		}
	}
	return out
}

// registerLoop периодически регистрирует нас во ВСЕХ сигналках под тегом сети ns и
// обновляет её таблицу пиров. Внешний адрес/кандидаты — общие для узла (один сокет),
// но каждая сеть объявляется своим тегом. Один узел ходит в сигналку N раз (по
// разу на сеть) — сети независимы, а сливать их в один запрос нельзя (разные теги).
func (s *Session) registerLoop(ns *netSession) {
	clients := make([]*signal.Client, 0, len(s.signalURLs))
	for _, u := range s.signalURLs {
		clients = append(clients, signal.NewClient(u))
	}

	hostname, _ := os.Hostname()
	req := proto.RegisterRequest{
		NetworkTag: ns.tag,
		PeerID:     s.peerID,
		Name:       hostname,
		VirtualIP:  s.selfIP.String(),
	}

	// prevPeerKey/lastPeerChange отслеживают смену состава участников: появление или
	// уход пира возвращает нас в разгонный темп, чтобы быстро пробить нового.
	var prevPeerKey string
	var lastPeerChange time.Time
	for {
		req.Endpoints = s.nodeEndpoints()

		type result struct {
			url   string
			peers []proto.PeerInfo
			seen  string
			err   error
		}
		results := make([]result, len(clients))

		var wg sync.WaitGroup
		for i, c := range clients {
			wg.Add(1)
			go func(i int, c *signal.Client) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				resp, err := c.Register(ctx, req)
				results[i] = result{url: s.signalURLs[i], err: err}
				if err == nil {
					results[i].peers = resp.Peers
					results[i].seen = resp.SeenFrom
				}
			}(i, c)
		}
		wg.Wait()

		merged := make(map[string]proto.PeerInfo)
		okCount := 0
		var errs []string
		up := make([]bool, len(results))
		peerSig := make(map[string][]bool)
		seenIP := ""
		for i, r := range results {
			if r.err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", short(r.url), r.err))
				continue
			}
			up[i] = true
			okCount++
			if seenIP == "" && r.seen != "" {
				seenIP = r.seen
			}
			for _, p := range r.peers {
				// Разные сигналки могут вернуть РАЗНЫЕ кандидаты одного пира (лаг
				// репликации): объединяем эндпоинты, а не затираем последним ответом,
				// иначе валидный путь от одной сигналки теряется из-за другой.
				if prev, ok := merged[p.PeerID]; ok {
					p.Endpoints = unionStrings(prev.Endpoints, p.Endpoints)
				}
				merged[p.PeerID] = p
				sl := peerSig[p.VirtualIP]
				if sl == nil {
					sl = make([]bool, len(results))
					peerSig[p.VirtualIP] = sl
				}
				sl[i] = true
			}
		}

		s.mu.Lock()
		if okCount == 0 {
			ns.signalErr = strings.Join(errs, "; ")
		} else {
			ns.signalErr = ""
		}
		ns.signalUp = up
		ns.peerSignals = peerSig
		if seenIP != "" {
			s.signalSeen = seenIP
		}
		eng := s.engine
		startedAt := s.startedAt
		s.mu.Unlock()

		if len(errs) > 0 {
			log.Printf("сеть %s: сигналка (%d из %d ответили): %s", tagShort(ns.tag), okCount, len(clients), strings.Join(errs, "; "))
		}
		if okCount > 0 && eng != nil {
			list := make([]proto.PeerInfo, 0, len(merged))
			for _, p := range merged {
				list = append(list, p)
			}
			eng.SyncPeers(ns.tagB, list)
		}

		// Заметили смену состава участников — перезапускаем окно разгона.
		key := peerSetKey(merged)
		now := time.Now()
		if key != prevPeerKey {
			prevPeerKey = key
			lastPeerChange = now
		}

		settled := eng != nil && eng.SettledForPolling()
		inBurst := now.Sub(startedAt) < burstWindow ||
			(!lastPeerChange.IsZero() && now.Sub(lastPeerChange) < burstWindow)
		// Разгоняемся в стартовом окне, пока не подключены все известные пиры
		// напрямую. Когда мы одни, settled=true, но опрашивать надо часто — иначе
		// первого соседа не увидим до registerFast. Когда все на прямом пути
		// (settled && пиры есть) — разгон не нужен, даже если окно ещё не вышло.
		burst := inBurst && !(settled && len(merged) > 0)
		next := registerFast
		switch {
		case burst:
			next = registerBurst // холодный старт/новый пир — быстро сводим адреса
		case settled && time.Since(startedAt) > settleWarmup:
			next = registerSlow
		}
		select {
		case <-ns.stop:
			return
		case <-ns.kick:
			// внешний адрес сменился — перерегистрируемся немедленно.
		case <-time.After(next):
		}
	}
}

// nodeEndpoints собирает наши текущие кандидаты (общие для узла): внешний адрес
// первым (сигналка режет на 8-м), затем альтернативные внешние без дублей, потом
// локальные. Обновляет s.selfEndpoint/extChurn (compare-and-set под mu дедуплицирует
// одинаковые изменения, приходящие из разных registerLoop-ов).
func (s *Session) nodeEndpoints() []string {
	s.endpointMu.Lock()
	defer s.endpointMu.Unlock()

	s.mu.Lock()
	eng := s.engine
	stunExt := s.stunExt
	localPort := s.localPort
	cur := s.selfEndpoint
	s.mu.Unlock()

	var liveStun, selfRefl, relayRaw string
	if eng != nil {
		if v, ok := eng.StunReflex(); ok {
			liveStun = v
		}
		if v, ok := eng.SelfReflex(); ok {
			selfRefl = v
		}
		if v, ok := eng.RelayReflex(); ok {
			relayRaw = v
		}
	}
	relayPub := ""
	if isPublicEndpoint(relayRaw) {
		relayPub = relayRaw
	}
	// Приоритет (последний непустой выигрывает): стартовый STUN (заморожен) <
	// peer-reflex (протухает при обрыве) < публичный relay-reflex < живой STUN.
	ext := stunExt
	for _, c := range []string{selfRefl, relayPub, liveStun} {
		if c != "" {
			ext = c
		}
	}
	// Гистерезис: если прежний внешний адрес всё ещё среди источников — держим его,
	// не переключаясь между одновременно валидными значениями. У symmetric NAT
	// relay-reflex и живой STUN видят РАЗНЫЕ порты (разный адресат → разный маппинг) —
	// без стикинеса ext скакал каждый раунд, накручивая extChurn и ложное «адрес
	// флапает», да ещё и рассылал пирам нестабильный self-candidate.
	if cur != "" {
		for _, c := range []string{stunExt, selfRefl, relayPub, liveStun} {
			if c == cur {
				ext = cur
				break
			}
		}
	}
	front := make([]string, 0, 4)
	seenEP := make(map[string]bool)
	for _, c := range []string{ext, liveStun, relayPub, selfRefl} {
		if c != "" && !seenEP[c] {
			front = append(front, c)
			seenEP[c] = true
		}
	}
	endpoints := append(front, localEndpoints(localPort)...)

	s.mu.Lock()
	if ext != "" && cur != "" && ext != cur {
		s.extChurn++
	}
	s.selfEndpoint = ext
	s.relaySeen = relayRaw
	s.mu.Unlock()
	if eng != nil {
		eng.SetSelfCandidates(endpoints)
	}
	return endpoints
}

// unionStrings объединяет два списка без дублей, сохраняя порядок (сначала a, затем
// новые из b). Пустые строки отбрасывает.
func unionStrings(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, s := range list {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// peerSetKey — стабильная сигнатура состава участников (отсортированные PeerID),
// чтобы registerLoop замечал появление/уход пира и включал разгонный темп.
func peerSetKey(m map[string]proto.PeerInfo) string {
	if len(m) == 0 {
		return ""
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, "|")
}

// tagShort — первые 8 hex-символов тега сети для логов. Имя сети в лог НЕ пишем:
// лог по умолчанию уходит на сигналку, а имя/пароль сети сервер знать не должен
// (см. proto — тег несекретен и невосстановим, а имя восстановимо).
func tagShort(tag string) string {
	if len(tag) > 8 {
		return tag[:8] + "…"
	}
	return tag
}

// short укорачивает URL до хоста.
func short(u string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i > 0 {
		s = s[:i]
	}
	return s
}

// logLoop (уровень узла) раз в logFlushInterval сливает накопленный лог на сигналку
// под тегами ВСЕХ сетей — диагностику узла можно прочитать через любую из них.
//
// Запрос уходит ТОЛЬКО когда есть новые строки. Ошибки отправки не логируем — иначе
// неудача породила бы новую строку, которую снова попытаемся отправить, по кругу;
// вместо этого возвращаем строки в буфер и пробуем в следующий раз.
func (s *Session) logLoop(nodeStop <-chan struct{}, hostname string) {
	s.mu.Lock()
	buf := s.logs
	s.mu.Unlock()
	if buf == nil {
		return
	}
	clients := make([]*signal.Client, 0, len(s.signalURLs))
	for _, u := range s.signalURLs {
		clients = append(clients, signal.NewClient(u))
	}

	for {
		select {
		case <-nodeStop:
			return
		case <-time.After(logFlushInterval):
		}
		if !s.logUpload.Load() {
			continue
		}
		lines := buf.Drain()
		if len(lines) == 0 {
			continue
		}

		s.mu.Lock()
		peerID := s.peerID
		tags := make([]string, 0, len(s.nets))
		for _, ns := range s.nets {
			tags = append(tags, ns.tag)
		}
		s.mu.Unlock()
		if len(tags) == 0 {
			buf.PutBack(lines)
			continue
		}

		anyOK := false
		var wg sync.WaitGroup
		var okMu sync.Mutex
		for _, tag := range tags {
			req := proto.LogRequest{NetworkTag: tag, PeerID: peerID, Name: hostname, Lines: lines}
			for _, c := range clients {
				wg.Add(1)
				go func(c *signal.Client, req proto.LogRequest) {
					defer wg.Done()
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if c.SendLogs(ctx, req) == nil {
						okMu.Lock()
						anyOK = true
						okMu.Unlock()
					}
				}(c, req)
			}
		}
		wg.Wait()
		if !anyOK {
			buf.PutBack(lines)
		}
	}
}

// localEndpoints собирает "локальный_IPv4:порт" по пригодным интерфейсам —
// кандидаты на случай, если пиры окажутся в одной локалке. Мусор отсеиваем:
// loopback, link-local 169.254.x (APIPA) и 25.x (наш же виртуальный адаптер).
func localEndpoints(port int) []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil || ip4[0] == 25 {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d", ip4.String(), port))
	}
	return out
}

// LoadOrCreateIdentity читает PeerID из конфига или создаёт новый при первом запуске.
func LoadOrCreateIdentity() (proto.PeerID, error) {
	dir, err := os.UserConfigDir()
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
	if err := os.WriteFile(path, []byte(id.String()), 0600); err != nil {
		return id, err
	}
	return id, nil
}
