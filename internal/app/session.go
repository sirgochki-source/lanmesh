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
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/discovery/dhtdisc"
	"github.com/sirgochki-source/lanmesh/internal/logbuf"
	"github.com/sirgochki-source/lanmesh/internal/netcache"
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

	// Discovery — способ обнаружения сети ("signal", "dht", "dht+relay").
	Discovery string `json:"discovery"`
	// DHT — состояние обнаружения через DHT; nil для обычных сетей.
	DHT *DHTView `json:"dht,omitempty"`
}

// DHTView — состояние обнаружения через публичную DHT (для панели).
type DHTView struct {
	Nodes  int    `json:"nodes"`  // узлов DHT в таблице маршрутизации
	Found  int    `json:"found"`  // адресов-кандидатов в последнем раунде
	Probes int    `json:"probes"` // сколько адресов сейчас пробивается
	Rounds int    `json:"rounds"` // отработано раундов поиска
	Error  string `json:"error"`  // ошибка последнего раунда
}

// SignalView — состояние одной сигналки: хост и ответила ли она в последнем раунде.
type SignalView struct {
	Host string `json:"host"`
	Up   bool   `json:"up"`
}

// NetworkTag — hex-тег сети (имя+пароль+режим обнаружения). Тот же, что видит
// сигналка и что стоит в NetworkView.Tag: по нему интерфейс сопоставляет
// сохранённые профили с активными сетями.
func NetworkTag(name, password, discovery string) string {
	return signal.NetworkTag(crypto.DeriveNetworkKeyMode(name, password, discovery))
}

// Режимы обнаружения пиров.
const (
	// DiscoverySignal — как было: пиров сводят сигнальные серверы.
	DiscoverySignal = "signal"
	// DiscoveryDHT — обнаружение через публичную DHT сети BitTorrent, БЕЗ единого
	// обращения к серверам: ни сигналок, ни ретранслятора (ему сеть не показывает
	// даже свой тег). Плата — пара за симметричным NAT/CGNAT не соединится вовсе.
	DiscoveryDHT = "dht"
	// DiscoveryDHTRelay — то же обнаружение, но ретранслятор разрешён как запасной
	// путь для непробиваемых пар. Отдельный режим, а не настройка: разрешение вшито
	// в ключ сети, поэтому оно одинаково у всех участников по построению.
	DiscoveryDHTRelay = "dht+relay"
)

// usesDHT — ищет ли режим участников через DHT (а не через сигналки).
func usesDHT(mode string) bool { return mode == DiscoveryDHT || mode == DiscoveryDHTRelay }

// usesRelay — разрешён ли сети ретранслятор. Обычным сетям — да, как и раньше.
func usesRelay(mode string) bool { return mode != DiscoveryDHT }

// netSession — одна сеть на общем узле: имя+пароль (для инвайта), тег и её цикл.
type netSession struct {
	name      string
	password  string // для генерации инвайта; хранится только при Remember у GUI
	tag       string // hex — для сигналки/логов
	tagB      [32]byte
	key       [crypto.KeySize]byte // ключ сети — из него же выводится инфохэш DHT
	discovery string               // DiscoverySignal | DiscoveryDHT | DiscoveryDHTRelay
	stop      chan struct{}        // закрывается при выходе из сети
	kick      chan struct{}        // разбудить перерегистрацию (смена внешнего адреса)

	// Под mu. Доступность сигналок и ошибка — свои у каждой сети.
	signalUp    []bool
	peerSignals map[string][]bool
	signalErr   string

	// Под mu. Состояние DHT-обнаружения (только в режимах с DHT).
	dhtNodes  int    // узлов в таблице DHT; 0 после раундов = DHT недоступна
	dhtFound  int    // адресов-кандидатов, найденных в последнем раунде
	dhtErr    string // ошибка последнего раунда
	dhtRounds int    // сколько раундов уже отработало
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

	// savedPort/onPortChosen — постоянный локальный порт узла, см. PickPort и
	// SetPort. onPortChosen дёргается ровно тогда, когда PickPort решил, что выбор
	// нужно сохранить в конфиг (первый запуск или сохранённый порт занят) — GUI
	// пишет cfg.Port и сохраняет файл. Оба поля читает только bringUpNode при
	// подъёме узла, поэтому менять их нужно ДО первого AddNetwork/Start.
	savedPort    int
	onPortChosen func(port int)

	// cache — подтверждённые endpoint'ы пиров между запусками (см. internal/netcache).
	// Открывается один раз при создании сессии и живёт, пока жива сессия — узел
	// поднимается и снимается многократно, а кэш должен пережить каждый цикл.
	cache *netcache.Cache

	// dht — общий на узел DHT-узел; поднимается лениво, при первой сети в режиме
	// DiscoveryDHT, и снимается вместе с последней такой сетью. Держать его без
	// нужды незачем: это отдельный сокет и постоянный фоновый трафик.
	dht     *dhtdisc.Discoverer
	dhtRefs int

	engine     *peer.Engine
	conn       *net.UDPConn
	dev        *tun.Device
	nodeStop   chan struct{}   // останавливает горутины уровня узла (Run/logLoop/fanout)
	kickFanout []chan struct{} // kick-каналы всех сетей (fanout сигнала reflex)

	nets map[[32]byte]*netSession // сети по tagB

	// name — отображаемое имя узла (что видят пиры и панель); пусто = os.Hostname().
	// Защищено ОТДЕЛЬНЫМ nameMu, а не s.mu, чтобы nodeName() можно было звать из-под
	// s.mu (например, в State) без риска дедлока.
	nameMu sync.RWMutex
	name   string
}

// SetName задаёт отображаемое имя узла (пусто/пробелы = вернуться к hostname). Применяется
// к новым анонсам и State; уже идущий registerLoop подхватит имя на следующем переподключении.
func (s *Session) SetName(name string) {
	s.nameMu.Lock()
	s.name = strings.TrimSpace(name)
	s.nameMu.Unlock()

	// Движок рассылает имя пирам сам (FrameHello) — там, где сигналки нет, это
	// единственный способ доехать до чужой панели, в том числе при переименовании.
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if eng != nil {
		eng.SetSelfName(s.nodeName())
	}
}

// nodeName — текущее отображаемое имя: заданное пользователем или, если пусто, hostname.
func (s *Session) nodeName() string {
	s.nameMu.RLock()
	n := s.name
	s.nameMu.RUnlock()
	if n != "" {
		return n
	}
	h, _ := os.Hostname()
	return h
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
		cache:       netcache.Open(netcachePath()),
	}
}

// netcachePath — файл кэша подтверждённых endpoint'ов, рядом с identity/
// config.json (тот же UserConfigDir()/lanmesh, см. dhtNodesPath). Кэш НЕ
// шифруется сознательно: config.json рядом хранит пароли сетей открытым
// текстом, так что шифрование соседнего файла с адресами ничего не защитило бы.
func netcachePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "lanmesh", "endpoints.json")
}

// UseRelay задаёт ретранслятор ("host:port") — общий на все сети. Вызывать до Start.
func (s *Session) UseRelay(addr string) {
	s.mu.Lock()
	s.relayAddr = addr
	s.mu.Unlock()
}

// SetPort задаёт сохранённый в конфиге порт узла и колбэк, которым сессия сообщает
// наружу порт, который нужно сохранить (первый запуск или сохранённый порт занят —
// см. PickPort). Вызывать до Start/AddNetwork: bringUpNode читает оба поля ровно
// один раз при подъёме узла, смена на ходу под уже поднятым узлом не подхватится.
func (s *Session) SetPort(saved int, onChosen func(port int)) {
	s.mu.Lock()
	s.savedPort = saved
	s.onPortChosen = onChosen
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
	return s.AddNetworkMode(network, password, DiscoverySignal)
}

// AddNetworkMode — то же, но с явным способом обнаружения пиров: DiscoverySignal
// (сигналки) или DiscoveryDHT (публичная DHT, ни одного обращения к серверам).
// Режим задаётся на каждую сеть: сети с разными режимами спокойно живут на одном
// узле, деля сокет, адаптер и внешний адрес.
func (s *Session) AddNetworkMode(network, password, discovery string) error {
	if network == "" || password == "" {
		return errors.New("нужны имя сети и пароль")
	}
	if discovery == "" {
		discovery = DiscoverySignal
	}
	switch discovery {
	case DiscoverySignal, DiscoveryDHT, DiscoveryDHTRelay:
	default:
		return fmt.Errorf("неизвестный способ обнаружения %q", discovery)
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	key := crypto.DeriveNetworkKeyMode(network, password, discovery)
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
	ns := &netSession{
		name:      network,
		password:  password,
		tag:       tag,
		tagB:      tagB,
		key:       key,
		discovery: discovery,
		stop:      make(chan struct{}),
		kick:      make(chan struct{}, 1),
	}

	if usesDHT(discovery) {
		// Поднимаем DHT-узел ДО того, как сеть попадёт в таблицы: иначе при отказе
		// (сокет занят) пришлось бы разбирать полусобранное состояние.
		if err := s.acquireDHT(); err != nil {
			if broughtUp {
				s.tearDownNode()
			}
			return err
		}
	}

	// Разрешение на релей задаём атомарно с созданием сети, одним вызовом под локом
	// движка — иначе сеть без серверов на миг могла бы попасть в bind к релею.
	s.engine.AddNetworkRelay(tagB, sealer, network, usesRelay(discovery))
	s.mu.Lock()
	s.nets[tagB] = ns
	s.rebuildFanoutLocked()
	s.mu.Unlock()

	// Кэш заливаем ДО первого раунда сигналки/DHT: если адрес друга не менялся,
	// пробитие стартует на первой секунде, а не после ответа сервера — в этот
	// момент мы его ещё даже не спросили. PeerID на этом шаге неоткуда взять,
	// кроме собственного кэша прошлых сессий (сигналка не отвечала, а DHT отдаёт
	// голые адреса без id) — отсюда Cache.Peers.
	for _, id := range s.cache.Peers(tag) {
		if addrs := s.cache.Get(tag, id); len(addrs) > 0 {
			s.engine.AddProbes(tagB, addrs)
		}
	}

	if usesDHT(discovery) {
		go s.dhtLoop(ns)
	} else {
		go s.registerLoop(ns)
	}
	// Тег сети без серверов НЕ логируем: лог сливается в общий буфер и уходит на
	// сигналки соседних сетей, а этот тег там не должен появляться. Для обычной
	// сети тег и так уходит на её же сигналку штатно.
	if usesDHT(discovery) {
		log.Printf("сеть подключена (обнаружение: %s)", discovery)
	} else {
		log.Printf("сеть %s подключена (обнаружение: %s)", tagShort(tag), discovery)
	}
	return nil
}

// acquireDHT поднимает общий DHT-узел (или увеличивает счётчик ссылок на него).
// Вызывать под opMu.
func (s *Session) acquireDHT() error {
	s.mu.Lock()
	if s.dht != nil {
		s.dhtRefs++
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	d, err := dhtdisc.New(dhtNodesPath())
	if err != nil {
		return err
	}
	s.mu.Lock()
	// Пока поднимали (сокет, чтение кэша), другая сеть могла успеть поднять свой —
	// лишний закрываем, ссылки считаем на один.
	if s.dht != nil {
		s.dhtRefs++
		s.mu.Unlock()
		d.Close()
		return nil
	}
	s.dht = d
	s.dhtRefs = 1
	s.mu.Unlock()
	log.Printf("DHT: узел поднят на %s", d.Addr())
	return nil
}

// releaseDHT снимает ссылку на DHT-узел и гасит его, когда сетей в этом режиме не
// осталось. Вызывать под opMu.
func (s *Session) releaseDHT() {
	s.mu.Lock()
	if s.dht == nil {
		s.mu.Unlock()
		return
	}
	s.dhtRefs--
	if s.dhtRefs > 0 {
		s.mu.Unlock()
		return
	}
	d := s.dht
	s.dht, s.dhtRefs = nil, 0
	s.mu.Unlock()

	d.Close()
	log.Printf("DHT: узел снят")
}

// dhtNodesPath — файл кэша узлов DHT рядом с identity. Кэш делает вход в DHT
// быстрым и независимым от bootstrap-узлов: они нужны, по сути, лишь в первый раз.
func dhtNodesPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "lanmesh", "dht-nodes.dat")
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
	if usesDHT(ns.discovery) {
		s.releaseDHT()
		log.Printf("сеть отключена (обнаружение: %s)", ns.discovery) // тег DHT-сети в лог не пишем
	} else {
		log.Printf("сеть %s отключена", tagShort(tagHex))
	}
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
		if usesDHT(ns.discovery) {
			s.releaseDHT()
		}
	}
	s.tearDownNode()
}

// Диапазон для постоянного порта узла. НЕ эфемерный (у Windows 49152–65535):
// сохранённый оттуда порт после перезагрузки может оказаться занят посторонним
// приложением, которое система обслужила раньше нас.
const (
	portRangeLo = 20000
	portRangeHi = 40000
)

// PickPort выбирает локальный UDP-порт узла. Возвращает порт и признак «сохрани
// меня в конфиг».
//
// Постоянный порт нужен двум фичам: проброс на роутере иначе пересоздавался бы
// каждый запуск и засорял таблицу маппингов, а кэш endpoint'ов был бы наполовину
// бесполезен — друзья помнят прежний ip:port, а узел уже на другом.
//
// Занятый сохранённый порт НЕ перезаписывает конфиг: иначе второй экземпляр на
// той же машине (run-node2.cmd) при каждом старте угонял бы порт у первого.
func PickPort(saved int) (int, bool) {
	if saved != 0 && portFree(saved) {
		return saved, false
	}
	for i := 0; i < 20; i++ {
		p := portRangeLo + rand.IntN(portRangeHi-portRangeLo)
		if portFree(p) {
			return p, saved == 0
		}
	}
	return 0, false // сдаёмся на случайный от ОС; сохранять нечего
}

// portFree — свободен ли порт на всех интерфейсах. Проверяем тем же способом,
// каким потом слушаем (dual-stack), иначе проверка соврала бы.
func portFree(p int) bool {
	c, err := net.ListenUDP("udp", &net.UDPAddr{Port: p})
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// listenNode поднимает боевой UDP-сокет. Просим "udp" с неуказанным IP — Go
// ставит IPV6_V6ONLY=0, и один сокет обслуживает оба семейства. Фолбэк на udp4
// нужен там, где IPv6-стек отключён политикой или отсутствует: узел обязан
// работать ровно как раньше, а не падать при старте.
//
// Занятый порт — ОТДЕЛЬНЫЙ случай, не «нет IPv6». Раньше эта функция звалась
// только с port=0 (ОС сама выбирает свободный, конфликт в принципе невозможен),
// поэтому любая ошибка ЗАВЕДОМО означала отсутствие IPv6-стека. С постоянным
// портом (см. PickPort) конфликт стал возможен — и если завести его в тот же
// фолбэк, узел молча потеряет IPv6 из-за случайной коллизии порта, хотя стек
// рабочий, а причина в лог не попадёт (только неверное "работаем только по
// IPv4"). Разводим: занятый порт возвращаем как явную ошибку, не трогая udp4.
//
// Проверено эмпирически (net.ListenUDP на второй бинд того же порта, см.
// port_test.go/TestListenNodeBusyPortReturnsError): реальная ошибка Windows —
// syscall.Errno(10048), то есть windows.WSAEADDRINUSE. syscall.EADDRINUSE — это
// ВЫМЫШЛЕННАЯ кросс-платформенная константа Go (APPLICATION_ERROR+2), которую
// сетевые вызовы на Windows никогда не возвращают: errors.Is с ней всегда false.
func listenNode(port int) (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err == nil {
		return conn, nil
	}
	if errors.Is(err, windows.WSAEADDRINUSE) {
		return nil, fmt.Errorf("порт %d занят: %w", port, err)
	}
	log.Printf("dual-stack сокет недоступен (%v) — работаем только по IPv4", err)
	return net.ListenUDP("udp4", &net.UDPAddr{Port: port})
}

// bringUpNode поднимает общий узел: сокет, STUN, адаптер, движок и его фоновые
// горутины. Вызывать под opMu, БЕЗ mu (STUN и адаптер медленные).
func (s *Session) bringUpNode() error {
	selfID, err := LoadOrCreateIdentity()
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	selfIP := proto.VirtualIP(selfID)

	s.mu.Lock()
	savedPort := s.savedPort
	s.mu.Unlock()
	port, savePort := PickPort(savedPort)
	conn, err := listenNode(port)
	if err != nil && errors.Is(err, windows.WSAEADDRINUSE) {
		// TOCTOU между PickPort и этим вызовом: PickPort проверяет свободность
		// пробным bind'ом и сразу закрывает сокет, а listenNode биндит заново —
		// в этом окне порт мог перехватить кто-то другой (например, второй
		// экземпляр lanmesh, стартующий в ту же секунду). До постоянного порта
		// (см. PickPort) listenNode(0) практически не мог вернуть эту ошибку —
		// теперь может, и без перевыбора узел падал бы там, где раньше просто
		// брал другой порт. Перевыбираем один раз и пробуем снова: PickPort сам
		// заново проверит portFree и обойдёт порт, который уже занят.
		log.Printf("порт %d перехвачен между проверкой и bind — перевыбираем", port)
		port, savePort = PickPort(savedPort)
		conn, err = listenNode(port)
	}
	if err != nil {
		return fmt.Errorf("udp listen: %w", err)
	}
	localPort := conn.LocalAddr().(*net.UDPAddr).Port
	if savePort {
		s.mu.Lock()
		cb := s.onPortChosen
		s.mu.Unlock()
		if cb != nil {
			cb(localPort)
		}
	}

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
	// Имя узла движку: пирам, узнанным без сигналки (DHT), его больше неоткуда
	// взять — они получат его кадром FrameHello.
	engine.SetSelfName(s.nodeName())
	// Только ПОДТВЕРЖДЁННЫЙ прямой адрес (движок зовёт колбэк лишь когда пир
	// реально ответил расшифрованным кадром) — кандидатов в кэш класть нельзя,
	// иначе он накопит мусор из DHT и будет воспроизводить его при каждом
	// старте. Put дешёвый (память, без диска) — колбэк в горячем пути чтения
	// не блокирует.
	engine.OnDirectConfirmed(func(tag [32]byte, id proto.PeerID, addr netip.AddrPort) {
		s.cache.Put(hex.EncodeToString(tag[:]), id.String(), addr.String())
	})

	s.mu.Lock()
	relayAddr := s.relayAddr
	s.mu.Unlock()
	if relayAddr != "" {
		if raddr, err := net.ResolveUDPAddr("udp4", relayAddr); err != nil {
			log.Printf("ретранслятор %s не разрешился (%v) — только прямые соединения", relayAddr, err)
		} else {
			// Нормализацию mapped-формы (резолвер отдаёт IPv4 как ::ffff:a.b.c.d)
			// делает сам UseRelay — инвариант живёт там же, где используется.
			engine.UseRelay(raddr.AddrPort())
			log.Printf("ретранслятор: %s (%s)", relayAddr, raddr)
		}
	}

	nodeStop := make(chan struct{})
	kick := engine.ReflexNotify() // берём ДО Run, чтобы не пропустить ранний сигнал

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
	go s.logLoop(nodeStop)
	go s.cacheSaveLoop(nodeStop)

	log.Printf("узел поднят, виртуальный IP %s", selfIP)
	return nil
}

// cacheSaveInterval — как часто сбрасываем кэш подтверждённых endpoint'ов на
// диск. Не на каждое подтверждение (см. OnDirectConfirmed выше) — файл не
// должен стать источником дисковой нагрузки под игровым трафиком.
const cacheSaveInterval = time.Minute

// cacheSaveLoop периодически сохраняет кэш endpoint'ов, пока узел поднят.
// Последний сброс — в tearDownNode, чтобы самое свежее подтверждение перед
// выходом не потерялось.
func (s *Session) cacheSaveLoop(nodeStop <-chan struct{}) {
	ticker := time.NewTicker(cacheSaveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-nodeStop:
			return
		case <-ticker.C:
			if err := s.cache.Save(); err != nil {
				log.Printf("кэш endpoint'ов: сохранение не удалось: %v", err)
			}
		}
	}
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

	// nodeStop закрываем ДО финального Save, чтобы cacheSaveLoop перестал будить
	// свой тикер — но close() не ждёт саму горутину: если она в этот момент
	// внутри Save(), финальный вызов ниже стартует ПАРАЛЛЕЛЬНО с ней, а не после.
	// Файловая часть Save (WriteFile+Rename) намеренно идёт вне c.mu (см.
	// netcache.go), поэтому без отдельного мьютекса записи два одновременных
	// Save писали бы один и тот же path+".tmp" — Save сериализует эту часть сам
	// (saveMu), так что параллельный вызов отсюда безопасен; порядок с close()
	// оставлен просто для предсказуемости, а не ради корректности.
	if nodeStop != nil {
		close(nodeStop)
	}
	if err := s.cache.Save(); err != nil {
		log.Printf("кэш endpoint'ов: сохранение при выходе не удалось: %v", err)
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

	st := StateView{Running: s.up, SelfName: s.nodeName(), Error: s.lastErr}
	if !s.up {
		return st
	}
	st.SelfIP = s.selfIP.String()
	st.SelfEndpoint = s.selfEndpoint
	st.StunVia = s.stunVia
	st.UptimeSec = int64(time.Since(s.startedAt).Seconds())

	for _, ns := range s.nets {
		nv := NetworkView{Name: ns.name, Tag: ns.tag, SignalError: ns.signalErr, Discovery: ns.discovery}
		if usesDHT(ns.discovery) {
			// Сигнальных точек у такой сети нет вовсе — показывать их серыми было бы
			// враньём: мы туда не ходим, а не «не дозвонились».
			nv.SignalError = ""
			nv.DHT = &DHTView{Nodes: ns.dhtNodes, Found: ns.dhtFound, Rounds: ns.dhtRounds, Error: ns.dhtErr}
			if s.engine != nil {
				nv.DHT.Probes = s.engine.ProbeCount(ns.tagB)
			}
		} else {
			nv.Signals = make([]SignalView, len(s.signalURLs))
			for i, u := range s.signalURLs {
				nv.Signals[i] = SignalView{Host: short(u), Up: i < len(ns.signalUp) && ns.signalUp[i]}
			}
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
		if usesDHT(ns.discovery) {
			continue // см. logLoop: тег DHT-сети сигналкам не показываем
		}
		tags = append(tags, ns.tag)
	}
	s.mu.Unlock()

	if !up || len(tags) == 0 {
		return "", errors.New("нет ни одной сети с обычным обнаружением (через DHT диагностика не отправляется)")
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

	nodeName := s.nodeName()
	anyOK := false
	var wg sync.WaitGroup
	var okMu sync.Mutex
	for _, tag := range tags {
		req := proto.LogRequest{NetworkTag: tag, PeerID: peerID, Name: nodeName, Lines: lines}
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
		// Диагностика уходит в общий буфер лога, а он потом сливается на сигналки
		// (см. logLoop/SendDiagnostics). Сеть без серверов сознательно избегает
		// сигналок — её имя и пиров сюда писать нельзя, иначе они уедут на сигналки
		// ДРУГИХ сетей узла. Фильтр в отправке режет только адресатов, а не текст,
		// поэтому режем текст здесь. Тег DHT-сети даём лишь как обезличенную отметку
		// (он несекретен и невосстановим к имени/паролю).
		if usesDHT(nv.Discovery) {
			out = append(out, fmt.Sprintf("--- сеть %s: обнаружение %s, участников %d (детали не логируем)",
				tagShort(nv.Tag), nv.Discovery, len(nv.Peers)))
			continue
		}
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

	req := proto.RegisterRequest{
		NetworkTag: ns.tag,
		PeerID:     s.peerID,
		Name:       s.nodeName(),
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

		// Пока шёл раунд (до 10с в HTTP), сеть могли снять (RemoveNetwork/Stop). Тогда
		// писать состояние и звать SyncPeers нельзя: та же сеть (тот же тег) может
		// быть уже пересоздана заново с чистой таблицей, и наш устаревший ответ
		// подмешался бы в её состояние. Зеркалит проверку после d.Round в dhtLoop.
		select {
		case <-ns.stop:
			return
		default:
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

// Темп обнаружения через DHT. Раунд заметно дороже опроса сигналки (обход чужих
// узлов, десятки запросов), поэтому пока пиров нет — ищем часто, а как связь
// установилась — редко: дальше адреса поддерживают сами пиры (FrameAddr) и живой
// STUN, а DHT нужна лишь чтобы заметить нового участника или переоткрыться после
// смены адреса у обоих сразу.
const (
	dhtRoundFast = 45 * time.Second
	dhtRoundIdle = 3 * time.Minute
	dhtRoundSlow = 10 * time.Minute
	// dhtWarmupRounds — сколько первых раундов ищем часто, даже пока никого нет:
	// хватает на первичную сборку сети. Дальше, если пиров так и не появилось,
	// темп падает до dhtRoundIdle — постоянно долбить DHT на 45с, когда мы одни,
	// незачем (нас всё равно найдут по нашему же анонсу и пробьют, а их трафик
	// заведёт пира сам). Появился пир и путь устоялся — уходим на dhtRoundSlow.
	dhtWarmupRounds = 8
	// dhtSaveEvery — не чаще этого сбрасываем кэш узлов DHT на диск.
	dhtSaveEvery = 5 * time.Minute
	// dhtSelfMemory — сколько помним свои недавние внешние адреса, чтобы не пробивать
	// собственный устаревший анонс (запись в DHT живёт ~2ч, берём с запасом).
	dhtSelfMemory = 30 * time.Minute
)

// dhtLoop — цикл обнаружения сети через публичную DHT. Полная замена
// registerLoop: ни одного обращения к сигналкам, ни одного нашего сервера.
//
// Что происходит за раунд: считаем инфохэш сети на текущие сутки, анонсируем на
// нём свой внешний порт и забираем адреса тех, кто анонсировался тем же ключом.
// Адреса отдаём движку голыми кандидатами (AddProbes) — он их пробивает, а пир из
// них получается только когда придёт кадр, расшифрованный ключом сети.
func (s *Session) dhtLoop(ns *netSession) {
	s.mu.Lock()
	d := s.dht
	s.mu.Unlock()
	if d == nil {
		return
	}

	// Bootstrap — единственное место, где мы обращаемся к чужим «входным» узлам, и
	// то лишь пока не набран кэш. Отменяемый: выход из сети не должен ждать обхода.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ns.stop
		cancel()
	}()
	defer cancel()

	if err := d.Bootstrap(ctx); err != nil {
		log.Printf("DHT bootstrap: %v", err) // без тега сети — лог уходит на чужие сигналки
	}

	// recentSelf — наши недавно анонсированные внешние адреса. При смене внешнего
	// порта СТАРЫЙ наш анонс ещё какое-то время жив в чужих узлах DHT и вернётся в
	// выдаче — фильтровать только по ТЕКУЩЕМУ ext недостаточно, иначе движок начнёт
	// пробивать собственный устаревший адрес. Держим адрес dhtSelfMemory, с запасом
	// перекрывая типичное время жизни записи.
	recentSelf := make(map[string]time.Time)

	for {
		// Внешний адрес держим свежим сами: в этом режиме registerLoop, который
		// обычно зовёт nodeEndpoints, не работает, а движку нужны наши кандидаты
		// для госсипа, и нам — актуальный порт для анонса.
		s.nodeEndpoints()

		s.mu.Lock()
		ext := s.selfEndpoint
		eng := s.engine
		s.mu.Unlock()

		port := 0
		if _, p, err := net.SplitHostPort(ext); err == nil {
			if v, err := strconv.Atoi(p); err == nil {
				port = v
			}
		}

		found, err := d.Round(ctx, ns.key, port)
		select {
		case <-ns.stop:
			return
		default:
		}

		// Запоминаем текущий свой адрес и чистим протухшие из памяти.
		roundNow := time.Now()
		if ext != "" {
			recentSelf[ext] = roundNow
		}
		for a, t := range recentSelf {
			if roundNow.Sub(t) > dhtSelfMemory {
				delete(recentSelf, a)
			}
		}

		// Себя из выдачи убираем: свой анонс вернётся нам первым, а долбить
		// собственный внешний адрес — впустую, а через NAT-петлю ещё и эхо. Режем
		// не только текущий ext, но и недавние свои адреса (при смене порта старый
		// наш анонс ещё жив в DHT).
		fresh := make([]string, 0, len(found))
		for _, a := range found {
			if a == ext {
				continue
			}
			if _, self := recentSelf[a]; self {
				continue
			}
			fresh = append(fresh, a)
		}
		if eng != nil && len(fresh) > 0 {
			eng.AddProbes(ns.tagB, fresh)
		}

		s.mu.Lock()
		ns.dhtNodes = d.NumNodes()
		ns.dhtFound = len(fresh)
		ns.dhtRounds++
		if err != nil {
			ns.dhtErr = err.Error()
		} else if ns.dhtNodes == 0 {
			// Ноль узлов после честного обхода — почти наверняка DHT не пропускает
			// провайдер или файрвол: сама по себе она за минуты набирает сотни.
			ns.dhtErr = "DHT недоступна: ни одного узла (провайдер или файрвол режет?)"
		} else {
			ns.dhtErr = ""
		}
		s.mu.Unlock()

		if err := d.SaveNodes(dhtSaveEvery); err != nil {
			log.Printf("DHT: кэш узлов не сохранён: %v", err)
		}

		hasPeers := eng != nil && len(eng.PeerViews(ns.tagB)) > 0
		next := dhtRoundFast
		switch {
		case hasPeers && eng.SettledForPolling():
			next = dhtRoundSlow // все пиры на свежем прямом пути — искать почти незачем
		case !hasPeers && ns.dhtRounds > dhtWarmupRounds:
			next = dhtRoundIdle // никого так и не нашли — снижаем темп, не долбим DHT впустую
		}
		select {
		case <-ns.stop:
			return
		case <-ns.kick:
			// Внешний адрес сменился — надо переанонсироваться с новым портом,
			// иначе в DHT будет висеть мёртвая запись до её протухания.
		case <-time.After(next):
		}
	}
}

// pickExternal выбирает внешний адрес узла из доступных источников.
//
// Приоритет (последний непустой выигрывает): стартовый STUN (заморожен) <
// peer-reflex (протухает при обрыве) < публичный relay-reflex < живой STUN.
//
// Гистерезис: если прежний адрес cur всё ещё среди ЖИВЫХ источников — держим его,
// не переключаясь между одновременно валидными значениями. У symmetric NAT
// relay-reflex и живой STUN видят РАЗНЫЕ порты (разный адресат → разный маппинг),
// и без стикинеса ext скакал бы каждый раунд, накручивая churn и рассылая пирам
// нестабильный self-candidate.
//
// ВАЖНО: stunExt в гистерезис НЕ входит. Это замороженный снимок STUN с момента
// старта узла, он не обновляется, а cur инициализируется тем же значением — так что
// stunExt==cur на каждом вызове, и его присутствие в списке намертво приклеивало бы
// ext к стартовому порту, глуша живой переопрос STUN. В signal-режиме это терпимо
// (на сигналку уходит весь список кандидатов), а в DHT анонсируется РОВНО ОДИН
// порт — заморозка означала бы вечный анонс мёртвого адреса после смены маппинга.
// На cone NAT liveStun==stunExt, так что cur всё равно совпадёт с liveStun и адрес
// остаётся стабильным — стикинес не теряется там, где он нужен.
func pickExternal(cur, stunExt, selfRefl, relayPub, liveStun string) string {
	ext := stunExt
	for _, c := range []string{selfRefl, relayPub, liveStun} {
		if c != "" {
			ext = c
		}
	}
	if cur != "" {
		for _, c := range []string{selfRefl, relayPub, liveStun} {
			if c == cur {
				return cur
			}
		}
	}
	return ext
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
	ext := pickExternal(cur, stunExt, selfRefl, relayPub, liveStun)
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
func (s *Session) logLoop(nodeStop <-chan struct{}) {
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
			// Сети в режиме DHT сознательно НЕ отправляют диагностику: смысл режима
			// в том, что о такой сети сигнальные серверы не знают вообще, а тег в
			// запросе выдал бы им и её существование, и доступ к её логам.
			if usesDHT(ns.discovery) {
				continue
			}
			tags = append(tags, ns.tag)
		}
		s.mu.Unlock()
		if len(tags) == 0 {
			buf.PutBack(lines)
			continue
		}

		nodeName := s.nodeName()
		anyOK := false
		var wg sync.WaitGroup
		var okMu sync.Mutex
		for _, tag := range tags {
			req := proto.LogRequest{NetworkTag: tag, PeerID: peerID, Name: nodeName, Lines: lines}
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

// Teredo (2001:0000::/32) и 6to4 (2002::/16, устаревший механизм — см. RFC 7526) —
// это IPv6-адреса, туннелированные поверх IPv4/UDP именно для прохода СКВОЗЬ NAT.
// Смысл добавления IPv6 в кандидаты — что при нативном IPv6 NAT нет и адрес
// интерфейса совпадает с внешним, пробивать нечего. Для этих двух туннелей
// посылка «NAT нет» ложна: пакет всё равно идёт через чужой релей с
// непредсказуемыми задержкой и надёжностью, а слот в кандидатах (потолок
// maxCandidates=12 в internal/peer) вытесняет рабочие адреса. Поэтому исключаем
// их явно, а не полагаемся на IsGlobalUnicast — он для обоих возвращает true.
var (
	ipv6TeredoPrefix    = netip.MustParsePrefix("2001:0000::/32")
	ipv6SixToFourPrefix = netip.MustParsePrefix("2002::/16")
)

// isTunneledIPv6 сообщает, что addr — Teredo или 6to4 (см. комментарий выше).
func isTunneledIPv6(addr netip.Addr) bool {
	return ipv6TeredoPrefix.Contains(addr) || ipv6SixToFourPrefix.Contains(addr)
}

// isEligibleLocalIP решает, годится ли адрес сетевого интерфейса в кандидаты
// localEndpoints. Отсеиваем: loopback, link-local (169.254.x/APIPA и fe80::),
// 25.x (наш же виртуальный адаптер), приватные/ULA-адреса IPv6 (fc00::/7 — не
// маршрутизируется в интернете) и туннели Teredo/6to4 (см. isTunneledIPv6).
func isEligibleLocalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] != 25 // наш же виртуальный адаптер
	}
	// IPv6: берём только глобальные юникасты, не приватные и не туннели.
	// STUN для них не нужен — NAT нет, адрес интерфейса и есть внешний адрес.
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip.To16())
	if !ok {
		return true // не смогли разобрать как netip.Addr — ведём себя как раньше
	}
	return !isTunneledIPv6(addr)
}

// formatEndpoint форматирует ip:port кандидата. Для IPv6 адрес обязан быть в
// квадратных скобках — без них netip.ParseAddrPort на приёмной стороне не
// сможет отличить разделитель адреса от разделителя порта.
func formatEndpoint(ip net.IP, port int) string {
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("%s:%d", ip4.String(), port)
	}
	return fmt.Sprintf("[%s]:%d", ip.String(), port)
}

// localEndpoints собирает "локальный_адрес:порт" (IPv4 и глобальные IPv6) по
// пригодным интерфейсам — кандидаты на случай, если пиры окажутся в одной
// локалке или у обоих есть нативный IPv6. Отбор адресов — в isEligibleLocalIP.
func localEndpoints(port int) []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || !isEligibleLocalIP(ipnet.IP) {
			continue
		}
		out = append(out, formatEndpoint(ipnet.IP, port))
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
