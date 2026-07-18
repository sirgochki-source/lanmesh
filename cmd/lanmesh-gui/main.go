//go:build windows

// Command lanmesh-gui — графическая оболочка над сеансом lanmesh.
//
// Двойной клик по exe: приложение поднимается с правами администратора (нужно
// для сетевого адаптера), открывает в браузере локальную панель управления и
// живёт в системном трее. Панель показывает сеть и её участников и даёт
// подключаться/отключаться без командной строки.
package main

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/sirgochki-source/lanmesh/internal/app"
	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/logbuf"
	"github.com/sirgochki-source/lanmesh/internal/signal"
)

// netTag — hex-тег сети из имени+пароля (тот же, что у сессии/сигналки). Нужен,
// чтобы сопоставлять сети панели (она шлёт тег) с сохранёнными профилями.
func netTag(name, password string) string {
	return signal.NetworkTag(crypto.DeriveNetworkKey(name, password))
}

//go:embed index.html
var indexHTML []byte

const (
	listenAddr = "127.0.0.1:8737"
	// defaultRelayAddr — запасной путь для пиров за симметричным NAT (в частности,
	// за мобильным CGNAT), где прямое пробитие невозможно в принципе. ПЛЕЙСХОЛДЕР:
	// впиши адрес своего ретранслятора (cmd/lanmesh-relay) в настройках панели или
	// в config.json — сюда свои боевые адреса не коммитим.
	defaultRelayAddr = "relay.example.com:25555"
	ifaceName        = "lanmesh"
)

// defaultSignalURLs — ВСЕ сигналки сразу, а не «основная и запасная»: клиент
// объявляется в каждой и сливает списки участников. Дефолт; пользователь может
// переопределить список в настройках панели.
//
// Своя на orangepi нужна не для красоты: у части провайдеров DPI режет
// `*.workers.dev` по имени в ClientHello — TCP встаёт, а рукопожатие рвут, и
// клиент получает EOF. Браузер это скрывает (прячет SNI через ECH), curl и Go —
// нет. Обратное тоже бывает: домашний сервер лежит, а Cloudflare жив.
//
// Своя идёт дважды: 25557 под TLS (по HTTP тег сети виден по дороге, а тег —
// ключ на чтение чужой диагностики через /logs) и 25556 плайнтекстом ради
// клиентов старых сборок. Второй строчке жить, пока все не обновятся.
//
// ПЛЕЙСХОЛДЕРЫ: подставь свои сигналки (Cloudflare Worker из worker/ и/или свой
// сервер cmd/lanmesh-signal) в настройках панели или в config.json. Боевые адреса
// в репозиторий не коммитим.
var defaultSignalURLs = []string{
	"https://your-worker.example.workers.dev",
	"https://your-server.example.com:25557",
	"http://your-server.example.com:25556",
}

var (
	// sess собирается в main() из эффективных серверов (конфиг или дефолт), а не
	// на этапе init — до загрузки config.json список сигналок ещё не известен.
	sess  *app.Session
	cfgMu sync.Mutex
	cfg   Config
)

// NetProfile — одна сохранённая сеть (мультисеть «как Radmin»). Пароль храним,
// иначе автоподключение и повторный вход невозможны; config.json пишется 0600.
type NetProfile struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// Config — сохранённые настройки. Networks — список сетей, к которым узел
// подключается при старте. Legacy-поля Network/Password/Remember читаются со
// старых конфигов и мигрируются в Networks (см. loadConfig).
type Config struct {
	Networks []NetProfile `json:"networks,omitempty"`

	// Legacy (одна сеть) — только для миграции.
	Network  string `json:"network,omitempty"`
	Password string `json:"password,omitempty"`
	Remember bool   `json:"remember,omitempty"`

	// SendLogs — отправлять ли диагностику на сигналку. Указатель, чтобы отличить
	// «выключено» от «в старом конфиге поля не было»: по умолчанию включено.
	SendLogs *bool `json:"sendLogs,omitempty"`
	// Signals — переопределённый список сигналок; пусто = defaultSignalURLs.
	Signals []string `json:"signals,omitempty"`
	// Relay — переопределённый ретранслятор; nil = defaultRelayAddr, "" = без relay.
	Relay *string `json:"relay,omitempty"`
}

// addNetwork добавляет сеть в список без дублей (по имени). Вызывать под cfgMu.
func (c *Config) addNetwork(name, password string) {
	for i, p := range c.Networks {
		if p.Name == name {
			c.Networks[i].Password = password
			return
		}
	}
	c.Networks = append(c.Networks, NetProfile{Name: name, Password: password})
}

// removeNetworkByTag убирает из списка сеть с данным hex-тегом. Вызывать под cfgMu.
func (c *Config) removeNetworkByTag(tag string) {
	out := c.Networks[:0]
	for _, p := range c.Networks {
		if netTag(p.Name, p.Password) != tag {
			out = append(out, p)
		}
	}
	c.Networks = out
}

// sendLogs — значение с учётом умолчания (включено, если не задано явно).
func (c Config) sendLogs() bool { return c.SendLogs == nil || *c.SendLogs }

// effectiveSignals — список сигналок с учётом умолчания.
func effectiveSignals(c Config) []string {
	if len(c.Signals) > 0 {
		return c.Signals
	}
	return defaultSignalURLs
}

// effectiveRelay — адрес ретранслятора с учётом умолчания.
func effectiveRelay(c Config) string {
	if c.Relay != nil {
		return *c.Relay
	}
	return defaultRelayAddr
}

func main() {
	logs := setupLogging()

	// Адаптер требует прав администратора — если их нет, перезапускаемся с UAC.
	ensureAdmin()

	cfgMu.Lock()
	cfg = loadConfig()
	c := cfg
	autoNets := append([]NetProfile(nil), cfg.Networks...)
	cfgMu.Unlock()

	sess = app.NewSession(effectiveSignals(c), nil, ifaceName)
	sess.EnableLogUpload(logs, c.sendLogs())
	sess.UseRelay(effectiveRelay(c))

	// Автоподключение ко всем сохранённым сетям (мультисеть). Первая поднимает
	// узел (STUN+адаптер), остальные добавляются в него мгновенно.
	for _, p := range autoNets {
		if err := sess.AddNetwork(p.Name, p.Password); err != nil {
			log.Printf("автоподключение %q: %v", p.Name, err)
		}
	}

	// Сокет открываем заранее, чтобы браузер стартовал только на готовый сервер.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("panel listen: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/state", handleState)
	mux.HandleFunc("/api/addnetwork", handleAddNetwork)
	mux.HandleFunc("/api/leavenetwork", handleLeaveNetwork)
	mux.HandleFunc("/api/disconnect", handleDisconnect)
	mux.HandleFunc("/api/sendlogs", handleSendLogs)
	mux.HandleFunc("/api/senddiag", handleSendDiag)
	mux.HandleFunc("/api/diagnose", handleDiagnose)
	mux.HandleFunc("/api/settings", handleSettings)
	mux.HandleFunc("/api/invite", handleInvite)

	go func() {
		if err := http.Serve(ln, mux); err != nil {
			log.Fatalf("panel serve: %v", err)
		}
	}()

	// Нативное окно (WebView2) на локальную панель — тот же UI, но не в браузере,
	// без вкладок и адресной строки. Держит главный поток до закрытия окна.
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug: false,
		WindowOptions: webview2.WindowOptions{
			Title:  "lanmesh",
			Width:  430,
			Height: 640,
			Center: true,
		},
	})
	if w == nil {
		log.Fatal("не удалось создать окно (нужен WebView2 Runtime — component из Microsoft Edge)")
	}
	defer w.Destroy()
	w.SetSize(360, 480, webview2.HintMin)
	w.Navigate("http://" + listenAddr)
	w.Run()
	sess.Stop()
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func handleState(w http.ResponseWriter, r *http.Request) {
	st := sess.State()

	cfgMu.Lock()
	sendLogs := cfg.sendLogs()
	cfgMu.Unlock()

	out := struct {
		app.StateView
		SendLogs bool `json:"sendLogs"`
	}{StateView: st, SendLogs: sendLogs}
	writeJSON(w, out, http.StatusOK)
}

// handleAddNetwork присоединяет сеть (и запоминает её в списке для автоподключения).
// Мультисеть: если уже есть другие сети, эта добавляется к ним, а не заменяет.
func handleAddNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Network  string `json:"network"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}
	req.Network = strings.TrimSpace(req.Network)
	if req.Network == "" || req.Password == "" {
		writeJSON(w, map[string]string{"error": "нужны имя сети и пароль"}, http.StatusBadRequest)
		return
	}

	if err := sess.AddNetwork(req.Network, req.Password); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusInternalServerError)
		return
	}

	cfgMu.Lock()
	cfg.addNetwork(req.Network, req.Password)
	saveConfig(cfg)
	cfgMu.Unlock()

	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}

// handleLeaveNetwork выходит из сети по её тегу и убирает её из сохранённого списка.
func handleLeaveNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}
	req.Tag = strings.TrimSpace(req.Tag)
	raw, err := hex.DecodeString(req.Tag)
	if err != nil || len(raw) != 32 {
		writeJSON(w, map[string]string{"error": "неверный тег"}, http.StatusBadRequest)
		return
	}
	var tagB [32]byte
	copy(tagB[:], raw)
	sess.RemoveNetwork(tagB)

	cfgMu.Lock()
	cfg.removeNetworkByTag(req.Tag)
	saveConfig(cfg)
	cfgMu.Unlock()

	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}

// handleSendLogs включает/выключает отправку диагностики. Действует сразу и
// переживает перезапуск (пишется в конфиг).
func handleSendLogs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}

	sess.SetLogUpload(req.Enabled)

	cfgMu.Lock()
	v := req.Enabled
	cfg.SendLogs = &v
	saveConfig(cfg)
	cfgMu.Unlock()

	if req.Enabled {
		log.Printf("отправка диагностики на сигналку включена")
	} else {
		log.Printf("отправка диагностики на сигналку выключена")
	}
	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}

// handleSendDiag немедленно заливает диагностику (лог + свежий снимок) на сигналки
// и возвращает тег сети — по нему её читают через /logs?net=<тег>.
func handleSendDiag(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tag, err := sess.SendDiagnostics(ctx)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error(), "tag": tag}, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"ok": "1", "tag": tag}, http.StatusOK)
}

// handleDiagnose гоняет пробу окружения (тип NAT, VPN-перехват, egress) и отдаёт
// её для показа в панели. Работает и без поднятой сети.
func handleDiagnose(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, sess.Diagnose(), http.StatusOK)
}

func handleDisconnect(w http.ResponseWriter, r *http.Request) {
	// Ответ отдаём и флашим ДО снятия адаптера: его закрытие ненадолго трогает
	// сетевой стек и может оборвать ещё не доставленный ответ.
	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	sess.Stop()
}

// handleSettings читает и меняет список серверов (сигналки + relay). Менять можно
// ТОЛЬКО пока сеть снята: registerLoop берёт снимок сигналок при старте.
func handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfgMu.Lock()
		c := cfg
		cfgMu.Unlock()
		// Сами адреса серверов НЕ отдаём в панель — чтобы личные сигналки/релей не
		// светились в UI (тултипы, поля настроек). Только метаданные: сколько
		// настроено и свои это или стандартные. Ввести кастомные всё равно можно.
		writeJSON(w, map[string]any{
			"custom":      len(c.Signals) > 0 || c.Relay != nil,
			"signalCount": len(effectiveSignals(c)),
			"hasRelay":    effectiveRelay(c) != "",
		}, http.StatusOK)
		return
	}

	var req struct {
		Signals []string `json:"signals"`
		Relay   string   `json:"relay"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}

	// Пустые строки/пробелы выкидываем; валидируем схему — иначе клиент молча
	// не достучится до кривого адреса и будет думать, что «сигналка лежит».
	var sigs []string
	for _, u := range req.Signals {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeJSON(w, map[string]string{"error": "сигналка должна начинаться с http:// или https:// — " + u}, http.StatusBadRequest)
			return
		}
		sigs = append(sigs, u)
	}
	relay := strings.TrimSpace(req.Relay)

	// Пустой список сигналок = вернуться к дефолту, а не остаться без связи вовсе.
	applySignals := sigs
	if len(applySignals) == 0 {
		applySignals = defaultSignalURLs
	}
	applyRelay := relay
	if applyRelay == "" {
		applyRelay = defaultRelayAddr
	}

	if err := sess.SetSignalURLs(applySignals); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusConflict)
		return
	}
	sess.UseRelay(applyRelay)

	cfgMu.Lock()
	if len(sigs) == 0 {
		cfg.Signals = nil // omitempty => чистый конфиг = дефолт
	} else {
		cfg.Signals = sigs
	}
	if relay == "" {
		cfg.Relay = nil
	} else {
		cfg.Relay = &relay
	}
	saveConfig(cfg)
	cfgMu.Unlock()

	log.Printf("настройки серверов обновлены: сигналок %d, relay %q", len(applySignals), applyRelay)
	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}

// handleInvite отдаёт ссылку-приглашение lanmesh://join?net=…&pass=… для сети с
// указанным тегом (?tag=<hex>). Имя+пароль берём из сохранённого списка сетей.
func handleInvite(w http.ResponseWriter, r *http.Request) {
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))

	cfgMu.Lock()
	var name, pass string
	for _, p := range cfg.Networks {
		if tag == "" || netTag(p.Name, p.Password) == tag {
			name, pass = p.Name, p.Password
			break
		}
	}
	cfgMu.Unlock()

	if name == "" {
		writeJSON(w, map[string]string{"link": "", "note": "сеть не найдена"}, http.StatusOK)
		return
	}
	link := "lanmesh://join?net=" + url.QueryEscape(name) + "&pass=" + url.QueryEscape(pass)
	writeJSON(w, map[string]string{"link": link}, http.StatusOK)
}

func writeJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// --- конфиг -----------------------------------------------------------------

func configFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "lanmesh", "config.json")
}

func loadConfig() Config {
	var c Config
	data, err := os.ReadFile(configFilePath())
	if err == nil {
		json.Unmarshal(data, &c)
	}
	// Миграция со старого одно-сетевого конфига: если список сетей пуст, но есть
	// сохранённая сеть с паролем (Remember) — переносим её в список.
	if len(c.Networks) == 0 && c.Network != "" && c.Password != "" && c.Remember {
		c.Networks = []NetProfile{{Name: c.Network, Password: c.Password}}
	}
	// Legacy-поля больше не нужны — список сетей теперь единственный источник.
	c.Network, c.Password, c.Remember = "", "", false
	return c
}

func saveConfig(c Config) {
	path := configFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("config mkdir: %v", err)
		return
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("config write: %v", err)
	}
}

// --- логи, права, браузер ---------------------------------------------------

// setupLogging направляет log в gui.log и параллельно в кольцевой буфер, из
// которого сеанс отправляет диагностику на сигналку. Возвращает этот буфер.
func setupLogging() *logbuf.Buffer {
	buf := logbuf.New(200)

	dir, err := os.UserConfigDir()
	if err != nil {
		log.SetOutput(buf)
		return buf
	}
	logDir := filepath.Join(dir, "lanmesh")
	os.MkdirAll(logDir, 0700)
	f, err := os.OpenFile(filepath.Join(logDir, "gui.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.SetOutput(buf)
		return buf
	}
	log.SetOutput(io.MultiWriter(f, buf))
	return buf
}

// isAdmin — истина, если процесс запущен с повышенными правами.
func isAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// ensureAdmin перезапускает приложение с запросом UAC, если прав не хватает.
func ensureAdmin() {
	if isAdmin() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	cwd, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	// SW_SHOWNORMAL = 1
	if err := windows.ShellExecute(0, verb, file, nil, cwd, 1); err != nil {
		log.Printf("elevation: %v", err)
		return // не вышло — продолжим без прав, ошибка всплывёт при Start
	}
	os.Exit(0) // управление ушло к elevated-копии
}
