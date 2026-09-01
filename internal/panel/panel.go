// Package panel — веб-панель управления сеансом lanmesh: сети, участники,
// настройки, диагностика, приглашения.
//
// Пакет переносимый. Оконная обвязка (WebView2, трей, UAC, автообновление)
// осталась в cmd/lanmesh-gui и живёт только под Windows, а сама панель одна и та
// же на обеих платформах: на Windows её показывает нативное окно, на Linux она
// открывается браузером. Так правка интерфейса не требует делать её дважды.
//
// БЕЗОПАСНОСТЬ. Панель слушает ТОЛЬКО 127.0.0.1, и адрес не настраивается — это
// сознательно. Аутентификации у неё нет, а API умеет заводить сети и отдаёт
// пароль сети в приглашении, поэтому выставить её наружу было бы дырой. Чтобы
// дотянуться до панели с другой машины, пробрасывают порт по SSH:
//
//	ssh -L 8737:127.0.0.1:8737 user@host
//
// От браузерной CSRF защищает guard; от других локальных процессов панель не
// защищена и защищаться не пытается (см. комментарий к guard).
package panel

import (
	"embed"
	"io/fs"
	"log"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"sync"

	"github.com/sirgochki-source/lanmesh/internal/logbuf"

	"github.com/sirgochki-source/lanmesh/internal/app"
)

// ListenAddr — единственный адрес, на котором живёт панель (см. «БЕЗОПАСНОСТЬ»).
// Порт вынесен отдельно, чтобы вызывающий мог его сменить, если 8737 занят;
// хост не настраивается принципиально.
const (
	Host        = "127.0.0.1"
	DefaultPort = 8737
)

//go:embed web
var webFS embed.FS

// Panel — состояние панели: сеанс, которым она управляет, и конфиг, который она
// читает и пишет. Раньше это были глобалы пакета main у GUI; теперь поля, иначе
// пакетом нельзя было бы пользоваться из второй команды.
type Panel struct {
	sess *app.Session
	cfg  Config
	// addr — на чём слушаем; нужен guard'у, чтобы сверять Origin.
	addr string
	// cfgMu защищает cfg: её правят и HTTP-хендлеры, и колбэк сохранения порта
	// из сеанса.
	cfgMu sync.Mutex
	// version — версия сборки для показа в панели. Задаётся вызывающим: у GUI
	// она своя (к ней привязано автообновление), у консольного клиента её может
	// не быть вовсе.
	version string
	// canUpdate — показывать ли в интерфейсе кнопку обновления. Автообновление
	// тянет .exe и существует только под Windows, поэтому маршруты
	// /api/checkupdate и /api/update регистрирует сам GUI, а панель лишь
	// сообщает фронту, есть ли они. Иначе кнопка на Linux вела бы в 404.
	canUpdate bool
}

// Options — что панель должна знать о вызывающем.
type Options struct {
	// Version — версия сборки для показа в интерфейсе; может быть пустой.
	Version string
	// CanUpdate — есть ли у сборки автообновление (Windows-only, см. Panel).
	CanUpdate bool
	// Logs — кольцевой буфер, из которого сеанс шлёт диагностику на сигналку.
	Logs *logbuf.Buffer
	// Iface — имя виртуального адаптера.
	Iface string
	// Port — порт ПАНЕЛИ; 0 означает DefaultPort.
	Port int
}

// Start поднимает сеанс по сохранённому конфигу и панель над ним: читает
// config.json, создаёт сессию на эффективных серверах, подключает автосохранение
// выбранного порта и подключается ко всем сохранённым сетям.
//
// Общая для окна (cmd/lanmesh-gui) и консольного клиента с -panel: раньше эта
// последовательность жила в main() у GUI, и Linux-клиенту пришлось бы её
// повторить — а разъехавшись, две копии дали бы разное поведение на платформах.
func Start(o Options) (*app.Session, *Panel) {
	port := o.Port
	if port == 0 {
		port = DefaultPort
	}
	cfg := loadConfig()

	sess := app.NewSession(effectiveSignals(cfg), nil, o.Iface)
	sess.EnableLogUpload(o.Logs, cfg.sendLogs())
	sess.UseRelay(effectiveRelay(cfg))
	sess.SetPortMap(cfg.portMap())
	sess.SetName(cfg.SelfName) // своё имя из конфига (пусто = hostname)

	p := &Panel{
		sess: sess, cfg: cfg, version: o.Version, canUpdate: o.CanUpdate,
		addr: fmt.Sprintf("%s:%d", Host, port),
	}

	// Постоянный порт УЗЛА: PickPort решает сам (первый запуск/занятый
	// сохранённый — не сохраняем), колбэк дёргается только когда решение нужно
	// записать.
	sess.SetPort(cfg.Port, p.SetPort)

	// Автоподключение ко всем сохранённым сетям (мультисеть). Первая поднимает
	// узел (STUN+адаптер), остальные добавляются в него мгновенно.
	for _, np := range cfg.Networks {
		if err := sess.AddNetworkMode(np.Name, np.Password, np.Discovery); err != nil {
			log.Printf("автоподключение %q: %v", np.Name, err)
		}
	}
	return sess, p
}

// Addr — адрес, который панель ожидает слушать (host:port).
func (p *Panel) Addr() string { return p.addr }

// Config возвращает копию текущего конфига (он меняется хендлерами).
func (p *Panel) Config() Config {
	p.cfgMu.Lock()
	defer p.cfgMu.Unlock()
	return p.cfg
}

// SetPort запоминает выбранный сеансом порт узла. Отдельный метод, потому что
// сеанс сообщает порт колбэком из своей горутины.
func (p *Panel) SetPort(port int) {
	p.cfgMu.Lock()
	p.cfg.Port = port
	saveConfig(p.cfg)
	p.cfgMu.Unlock()
}

// Routes регистрирует маршруты панели на mux. Маршруты автообновления сюда НЕ
// входят: они Windows-only, их добавляет cmd/lanmesh-gui поверх.
func (p *Panel) Routes(mux *http.ServeMux) {
	mux.Handle("/", staticHandler())
	mux.HandleFunc("/api/state", p.guard(p.handleState))
	mux.HandleFunc("/api/addnetwork", p.guard(p.handleAddNetwork))
	mux.HandleFunc("/api/leavenetwork", p.guard(p.handleLeaveNetwork))
	mux.HandleFunc("/api/disconnect", p.guard(p.handleDisconnect))
	mux.HandleFunc("/api/reconnect", p.guard(p.handleReconnect))
	mux.HandleFunc("/api/sendlogs", p.guard(p.handleSendLogs))
	mux.HandleFunc("/api/portmap", p.guard(p.handlePortMap))
	mux.HandleFunc("/api/senddiag", p.guard(p.handleSendDiag))
	mux.HandleFunc("/api/diagnose", p.guard(p.handleDiagnose))
	mux.HandleFunc("/api/settings", p.guard(p.handleSettings))
	mux.HandleFunc("/api/setname", p.guard(p.handleSetName))
	mux.HandleFunc("/api/invite", p.guard(p.handleInvite))
	mux.HandleFunc("/api/parseinvite", p.guard(p.handleParseInvite))
}

// Guard оборачивает чужой хендлер той же CSRF-защитой, что и свои. Нужен GUI для
// маршрутов автообновления, которые он регистрирует сам.
func (p *Panel) Guard(next http.HandlerFunc) http.HandlerFunc { return p.guard(next) }

// Типы MIME задаём явно: на Windows Go берёт их из реестра и может отдать .js
// как text/plain — тогда WebView2 (строгая проверка MIME для ES-модулей)
// откажется исполнять модули и панель не поднимется.
func init() {
	mime.AddExtensionType(".html", "text/html; charset=utf-8")
	mime.AddExtensionType(".css", "text/css; charset=utf-8")
	mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")
	mime.AddExtensionType(".svg", "image/svg+xml")
	mime.AddExtensionType(".json", "application/json; charset=utf-8")
}

// staticHandler отдаёт встроенные ассеты панели (web/): index.html и ES-модули.
// Ручки /api/* регистрируются отдельными, более специфичными паттернами и имеют
// приоритет над этим catch-all "/".
func staticHandler() http.Handler {
	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("web sub-fs: %v", err)
	}
	return http.FileServer(http.FS(webRoot))
}

// guard закрывает локальный API от браузерной CSRF: /api/* слушает 127.0.0.1, и
// пока панель запущена, ЛЮБАЯ открытая в обычном браузере вредоносная страница
// иначе могла бы дёргать эти ручки (подключить к чужой сети, сменить серверы,
// отключить). Пропускаем только запросы со своей же страницы: чужой Origin или
// Sec-Fetch-Site=cross-site/same-site отбиваем. Тело ограничиваем — заодно от DoS.
//
// Это НЕ защита от других локальных процессов (они выставят любой заголовок и так
// же могут прочитать эту же страницу) — для того нужен именованный pipe с ACL;
// здесь закрыт именно веб-вектор.
func (p *Panel) guard(next http.HandlerFunc) http.HandlerFunc {
	self := "http://" + p.addr
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && o != self {
			http.Error(w, "cross-origin запрещён", http.StatusForbidden)
			return
		}
		if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
			http.Error(w, "cross-site запрещён", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 МБ хватает любому нашему телу
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
