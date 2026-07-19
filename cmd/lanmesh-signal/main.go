// Command lanmesh-signal — сигнальный сервер lanmesh для самостоятельного
// хостинга (в паре с cmd/lanmesh-relay на том же ящике).
//
// Зачем, если есть воркер на Cloudflare: `*.workers.dev` у части провайдеров
// режет DPI, и клиент получает EOF на ровном месте. Браузер это скрывает (он
// прячет SNI через ECH), а Go так не умеет — имя летит открытым текстом. Своя
// сигналка на белом адресе от этого не зависит.
//
// Клиенты регистрируются СРАЗУ ВО ВСЕХ доступных сигналках и сливают списки, а
// не переключаются между ними: иначе сеть раскололась бы на тех, кто на одной, и
// тех, кто на другой, и они бы друг друга не видели.
//
// Слушателей два и оба обслуживают один и тот же реестр: HTTP на 25556 и HTTPS
// на 25557 (если заданы -tls-cert/-tls-key). Плайнтекст оставлен ради старых
// клиентов, у которых в дефолтах только он; глушить его можно, когда все
// обновятся. По HTTP видно тег сети и endpoint'ы — трафик это не раскрывает (он
// шифруется ключом сети, которого у сервера нет), но тег даёт доступ к чужой
// диагностике через /logs.
//
// Протокол — тот же, что у воркера (JSON поверх HTTP):
//
//	POST /register  {net,id,name,vip,eps}  -> {self,peers}
//	POST /log       {net,id,name,lines}    -> {ok,stored}
//	GET  /logs?net=<тег>                   -> текст
//	GET  /health                           -> "lanmesh signal ok"
//
// Состояние — в памяти: пиры перерегистрируются каждые 20с, так что после
// перезапуска таблица восстанавливается сама. Ключа сети сервер не знает и
// трафик расшифровать не может: видит только несекретный тег и endpoint'ы.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	peerTTL     = 60 * time.Second // ~3 пропущенных регистрации -> пир выпал
	logTTL      = time.Hour        // столько живёт диагностика
	logMaxLines = 200              // строк в одной пачке
	logMaxLine  = 500              // символов в строке
	logMaxKeep  = 2000             // строк на сеть — чтобы память не росла
	maxBody     = 256 << 10        // 256 КБ хватит с запасом
	sweepEvery  = 5 * time.Minute

	// Потолки реестра. Тег сети — несекретный хэш, и /register/log заводят запись на
	// ЛЮБОЙ корректный по формату тег без доказательства владения сетью. Без потолков
	// поток запросов со случайными тегами/ID раздул бы реестр (сети × пиры × логи) до
	// OOM ещё до первой уборки (sweepEvery). Значения с большим запасом над реальной
	// компанией друзей, но ограничивают память сверху.
	maxNets        = 1000
	maxPeersPerNet = 256
)

var (
	reTag = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reID  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reEP  = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}:\d{1,5}$`)
)

// --- модель -----------------------------------------------------------------

type peerInfo struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	VIP  string   `json:"vip"`
	EPs  []string `json:"eps"`
}

type peerRec struct {
	info  peerInfo
	seen  time.Time
	srcIP string // IP, с которого пришла регистрация (реальный адрес глазами сервера)
}

type logBatch struct {
	at    time.Time
	name  string
	peer  string
	lines []string
}

// network — состояние одной сети (по тегу).
type network struct {
	peers map[string]peerRec
	logs  []logBatch
}

type registry struct {
	mu   sync.Mutex
	nets map[string]*network
}

func newRegistry() *registry { return &registry{nets: make(map[string]*network)} }

// register кладёт пира и возвращает остальных живых участников той же сети.
// srcIP — реальный адрес запроса; по нему ловим коллизию (двое за одним
// внешним IP — общий VPN-выход/NAT: тогда сервер их не различит по адресу).
func (r *registry) register(tag string, self peerInfo, srcIP string) []peerInfo {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := r.nets[tag]
	if n == nil {
		if len(r.nets) >= maxNets {
			log.Printf("реестр переполнен (%d сетей) — регистрация %s отклонена", len(r.nets), self.ID[:8])
			return nil
		}
		n = &network{peers: make(map[string]peerRec)}
		r.nets[tag] = n
	}
	now := time.Now()
	if _, exists := n.peers[self.ID]; !exists && len(n.peers) >= maxPeersPerNet {
		log.Printf("сеть переполнена (%d узлов) — регистрация %s отклонена", len(n.peers), self.ID[:8])
		return nil
	}
	n.peers[self.ID] = peerRec{info: self, seen: now, srcIP: srcIP}

	out := make([]peerInfo, 0, len(n.peers))
	for id, rec := range n.peers {
		if now.Sub(rec.seen) > peerTTL {
			delete(n.peers, id) // протухших чистим тут же, отдельная уборка не нужна
			continue
		}
		if id != self.ID {
			out = append(out, rec.info)
			// Коллизия внешнего адреса: два разных PeerID с одного IP.
			if srcIP != "" && rec.srcIP == srcIP {
				log.Printf("коллизия src %s: %s и %s (общий VPN-выход/NAT?)", srcIP, self.ID[:8], id[:8])
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VIP < out[j].VIP })
	return out
}

func (r *registry) addLog(tag string, b logBatch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.nets[tag]
	if n == nil {
		if len(r.nets) >= maxNets {
			return // реестр переполнен — логи для новой сети не заводим
		}
		n = &network{peers: make(map[string]peerRec)}
		r.nets[tag] = n
	}
	n.logs = append(n.logs, b)
	trimLogs(n)
}

func (r *registry) dumpLogs(tag string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.nets[tag]
	if n == nil {
		return ""
	}
	trimLogs(n)

	var sb strings.Builder
	for _, b := range n.logs {
		who := b.name
		if who == "" {
			who = "?"
		}
		for _, l := range b.lines {
			sb.WriteString(who)
			sb.WriteString(" (")
			sb.WriteString(b.peer)
			sb.WriteString(")\t")
			sb.WriteString(l)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// trimLogs режет логи сначала по возрасту, потом по объёму: один болтливый
// клиент не должен раздуть память. Вызывать под локом.
func trimLogs(n *network) {
	cutoff := time.Now().Add(-logTTL)
	keep := n.logs[:0]
	for _, b := range n.logs {
		if b.at.After(cutoff) {
			keep = append(keep, b)
		}
	}
	n.logs = keep

	total := 0
	for _, b := range n.logs {
		total += len(b.lines)
	}
	for total > logMaxKeep && len(n.logs) > 0 {
		total -= len(n.logs[0].lines)
		n.logs = n.logs[1:]
	}
}

// sweep выкидывает сети, где давно никого нет.
func (r *registry) sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for tag, n := range r.nets {
		for id, rec := range n.peers {
			if now.Sub(rec.seen) > peerTTL {
				delete(n.peers, id)
			}
		}
		trimLogs(n)
		if len(n.peers) == 0 && len(n.logs) == 0 {
			delete(r.nets, tag)
		}
	}
}

func (r *registry) stats() (nets, peers int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.nets {
		nets++
		peers += len(n.peers)
	}
	return
}

// --- HTTP -------------------------------------------------------------------

func main() {
	addr := flag.String("listen", ":25556", "адрес HTTP (пусто — не слушать)")
	tlsAddr := flag.String("tls-listen", ":25557", "адрес HTTPS (пусто — не слушать)")
	tlsCert := flag.String("tls-cert", "", "файл сертификата (fullchain.pem); без него HTTPS не поднимается")
	tlsKey := flag.String("tls-key", "", "файл приватного ключа (privkey.pem)")
	flag.Parse()

	// PORT из окружения (Cloud Run, Render, Railway и т.п.): платформа сама
	// терминирует HTTPS и пробрасывает нам обычный HTTP на этот порт. В этом режиме
	// слушаем только его и не поднимаем свой TLS — сертификатами занимается платформа.
	if p := os.Getenv("PORT"); p != "" {
		*addr = ":" + p
		*tlsAddr = ""
	}

	reg := newRegistry()
	go func() {
		for range time.Tick(sweepEvery) {
			reg.sweep()
			nets, peers := reg.stats()
			log.Printf("статистика: сетей %d, узлов %d", nets, peers)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("lanmesh signal ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("lanmesh signal ok\n"))
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		handleRegister(reg, w, r)
	})
	mux.HandleFunc("/log", func(w http.ResponseWriter, r *http.Request) {
		handleLog(reg, w, r)
	})
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		handleLogs(reg, w, r)
	})

	newServer := func(a string) *http.Server {
		return &http.Server{
			Addr:              a,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       20 * time.Second,
			WriteTimeout:      20 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
	}

	tlsOn := *tlsAddr != "" && *tlsCert != "" && *tlsKey != ""
	if *addr == "" && !tlsOn {
		log.Fatal("нечего слушать: задайте -listen и/или -tls-listen с -tls-cert/-tls-key")
	}

	// Падаем при первой же ошибке любого из слушателей: половина сигналки —
	// это молчаливая половина сети, пусть лучше systemd перезапустит.
	errc := make(chan error, 2)
	if *addr != "" {
		go func() {
			log.Printf("lanmesh-signal слушает http %s", *addr)
			errc <- newServer(*addr).ListenAndServe()
		}()
	}
	if tlsOn {
		go func() {
			log.Printf("lanmesh-signal слушает https %s (сертификат %s)", *tlsAddr, *tlsCert)
			errc <- newServer(*tlsAddr).ListenAndServeTLS(*tlsCert, *tlsKey)
		}()
	} else if *tlsAddr != "" {
		log.Print("https выключен: не заданы -tls-cert/-tls-key")
	}
	log.Fatal(<-errc)
}

func handleRegister(reg *registry, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "нужен POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Net  string   `json:"net"`
		ID   string   `json:"id"`
		Name string   `json:"name"`
		VIP  string   `json:"vip"`
		EPs  []string `json:"eps"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !reTag.MatchString(req.Net) || !reID.MatchString(req.ID) {
		writeJSON(w, map[string]string{"error": "bad net/id"}, http.StatusBadRequest)
		return
	}

	self := peerInfo{
		ID:   req.ID,
		Name: sanitize(req.Name),
		VIP:  sanitize(req.VIP),
		EPs:  sanitizeEndpoints(req.EPs),
	}
	src := clientIP(r)
	peers := reg.register(req.Net, self, src)
	resp := map[string]any{"self": self, "peers": peers}
	if src != "" {
		resp["seen"] = src // реальный адрес клиента глазами сервера — для сверки с STUN
	}
	writeJSON(w, resp, http.StatusOK)
}

// clientIP — IP, с которого пришёл запрос. Сигналка на Pi проброшена портом
// напрямую (без обратного прокси), так что RemoteAddr — настоящий адрес клиента;
// заголовкам X-Forwarded-* тут не доверяем, их мог бы подставить сам клиент.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	return host
}

func handleLog(reg *registry, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "нужен POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Net   string   `json:"net"`
		ID    string   `json:"id"`
		Name  string   `json:"name"`
		Lines []string `json:"lines"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !reTag.MatchString(req.Net) || !reID.MatchString(req.ID) {
		writeJSON(w, map[string]string{"error": "bad net/id"}, http.StatusBadRequest)
		return
	}
	if len(req.Lines) == 0 {
		writeJSON(w, map[string]string{"error": "нет строк"}, http.StatusBadRequest)
		return
	}

	lines := req.Lines
	if len(lines) > logMaxLines {
		lines = lines[:logMaxLines]
	}
	cut := make([]string, 0, len(lines))
	for _, l := range lines {
		if len(l) > logMaxLine {
			l = l[:logMaxLine]
		}
		cut = append(cut, l)
	}

	reg.addLog(req.Net, logBatch{
		at:    time.Now(),
		name:  sanitize(req.Name),
		peer:  req.ID[:8],
		lines: cut,
	})
	writeJSON(w, map[string]any{"ok": true, "stored": len(cut)}, http.StatusOK)
}

func handleLogs(reg *registry, w http.ResponseWriter, r *http.Request) {
	tag := r.URL.Query().Get("net")
	if !reTag.MatchString(tag) {
		http.Error(w, "нужен ?net=<64 hex>\n", http.StatusBadRequest)
		return
	}
	out := reg.dumpLogs(tag)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if out == "" {
		out = "логов нет (клиенты не слали, истёк час или сервер перезапускался)\n"
	}
	w.Write([]byte(out))
}

// --- утилиты ----------------------------------------------------------------

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(v); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// sanitize — как в воркере: обрезаем и выкидываем всё, кроме печатного ASCII.
func sanitize(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	var sb strings.Builder
	for _, r := range s {
		if r >= 0x20 && r <= 0x7e {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func sanitizeEndpoints(eps []string) []string {
	out := make([]string, 0, 8)
	for _, e := range eps {
		if reEP.MatchString(e) {
			out = append(out, e)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}
