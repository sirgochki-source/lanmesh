//go:build windows

// Command lanmesh-win — нативное окно клиента lanmesh (Walk / Win32), замена
// веб-панели. Слой UI; бэкенд (app.Session) переиспользуется.
package main

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"golang.org/x/sys/windows"

	"github.com/sirgochki-source/lanmesh/internal/app"
	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/logbuf"
	"github.com/sirgochki-source/lanmesh/internal/peer"
	"github.com/sirgochki-source/lanmesh/internal/signal"
)

const ifaceName = "lanmesh"

// Плейсхолдер-дефолты (реальные адреса — в config.json, диалог «Серверы»).
var defaultSignals = []string{
	"https://your-worker.example.workers.dev",
	"https://your-server.example.com:25557",
	"http://your-server.example.com:25556",
}

const defaultRelay = "relay.example.com:25555"

// --- конфиг -----------------------------------------------------------------

type netProfile struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

type config struct {
	Networks []netProfile `json:"networks,omitempty"`
	Signals  []string     `json:"signals,omitempty"`
	Relay    *string      `json:"relay,omitempty"`
	// legacy — миграция со старого одно-сетевого конфига
	Network  string `json:"network,omitempty"`
	Password string `json:"password,omitempty"`
	Remember bool   `json:"remember,omitempty"`
}

var (
	cfg   config
	cfgMu sync.Mutex
)

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "lanmesh", "config.json")
}

func loadConfig() config {
	var c config
	if data, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(data, &c)
	}
	if len(c.Networks) == 0 && c.Network != "" && c.Password != "" && c.Remember {
		c.Networks = []netProfile{{Name: c.Network, Password: c.Password}}
	}
	c.Network, c.Password, c.Remember = "", "", false
	return c
}

func saveConfig() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	p := configPath()
	os.MkdirAll(filepath.Dir(p), 0700)
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(p, data, 0600)
}

func effSignals(c config) []string {
	if len(c.Signals) > 0 {
		return c.Signals
	}
	return defaultSignals
}

func effRelay(c config) string {
	if c.Relay != nil {
		return *c.Relay
	}
	return defaultRelay
}

func netTag(name, password string) string {
	return signal.NetworkTag(crypto.DeriveNetworkKey(name, password))
}

// --- глобальное состояние UI ------------------------------------------------

var (
	sess      *app.Session
	mw        *walk.MainWindow
	headerLbl *walk.Label
	statusLbl *walk.Label
	netTV     *walk.TableView
	memTV     *walk.TableView
	netMdl    *netModel
	memMdl    *memModel
)

func main() {
	ensureAdmin()

	logs := setupLogging()

	cfg = loadConfig()
	sess = app.NewSession(effSignals(cfg), nil, ifaceName)
	sess.EnableLogUpload(logs, true)
	sess.UseRelay(effRelay(cfg))
	for _, p := range cfg.Networks {
		go func(p netProfile) {
			if err := sess.AddNetwork(p.Name, p.Password); err != nil {
				log.Printf("автоподключение %q: %v", p.Name, err)
			}
		}(p)
	}

	netMdl = &netModel{}
	memMdl = &memModel{}

	win := MainWindow{
		AssignTo: &mw,
		Title:    "lanmesh",
		MinSize:  Size{Width: 520, Height: 560},
		Size:     Size{Width: 560, Height: 620},
		Layout:   VBox{},
		Children: []Widget{
			Label{AssignTo: &headerLbl, Text: "…"},
			Label{AssignTo: &statusLbl, Text: ""},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: "＋ Добавить сеть", OnClicked: onAddNetwork},
					PushButton{Text: "Пригласить", OnClicked: onInvite},
					PushButton{Text: "Выйти из сети", OnClicked: onLeave},
					HSpacer{},
					PushButton{Text: "Серверы", OnClicked: onSettings},
					PushButton{Text: "Диагностика", OnClicked: onDiagnose},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					GroupBox{
						Title:  "Сети",
						Layout: VBox{MarginsZero: true},
						MaxSize: Size{Width: 200, Height: 0},
						MinSize: Size{Width: 180, Height: 0},
						Children: []Widget{
							TableView{
								AssignTo:              &netTV,
								Model:                 netMdl,
								LastColumnStretched:   true,
								OnCurrentIndexChanged: refreshMembers,
								Columns: []TableViewColumn{
									{Title: "Сеть"},
									{Title: "Сигн.", Width: 46},
									{Title: "Уч.", Width: 34},
								},
							},
						},
					},
					GroupBox{
						Title:  "Участники",
						Layout: VBox{MarginsZero: true},
						Children: []Widget{
							TableView{
								AssignTo:            &memTV,
								Model:               memMdl,
								LastColumnStretched: true,
								ContextMenuItems: []MenuItem{
									Action{Text: "Скопировать IP", OnTriggered: onCopyPeerIP},
								},
								Columns: []TableViewColumn{
									{Title: "Имя", Width: 130},
									{Title: "IP", Width: 110},
									{Title: "Статус", Width: 90},
									{Title: "RTT", Width: 60},
								},
							},
						},
					},
				},
			},
		},
	}

	if err := win.Create(); err != nil {
		log.Fatalf("окно: %v", err)
	}
	if ic := loadIcon(); ic != nil {
		mw.SetIcon(ic)
	}
	setupTray()
	refresh()

	go func() {
		t := time.NewTicker(1200 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			mw.Synchronize(refresh)
		}
	}()

	mw.SetVisible(true)
	mw.Run()
	sess.Stop()
}

//go:embed icon.ico
var iconBytes []byte

func loadIcon() *walk.Icon {
	tmp := filepath.Join(os.TempDir(), "lanmesh-icon.ico")
	if err := os.WriteFile(tmp, iconBytes, 0600); err != nil {
		return nil
	}
	ic, err := walk.NewIconFromFile(tmp)
	if err != nil {
		return nil
	}
	return ic
}

var quitting bool

// setupTray заводит иконку в трее: закрытие окна прячет его в трей, выход — из меню.
func setupTray() {
	ni, err := walk.NewNotifyIcon(mw)
	if err != nil {
		return
	}
	if ic := loadIcon(); ic != nil {
		ni.SetIcon(ic)
	}
	ni.SetToolTip("lanmesh")

	open := walk.NewAction()
	open.SetText("Открыть")
	open.Triggered().Attach(showWindow)
	ni.ContextMenu().Actions().Add(open)

	exit := walk.NewAction()
	exit.SetText("Выход")
	exit.Triggered().Attach(func() {
		quitting = true
		ni.Dispose()
		mw.Close()
	})
	ni.ContextMenu().Actions().Add(exit)

	ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			showWindow()
		}
	})
	ni.SetVisible(true)

	// Закрытие окна = свернуть в трей, а не выйти (выход — через меню трея).
	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if !quitting {
			*canceled = true
			mw.Hide()
		}
	})
}

func showWindow() {
	mw.Show()
	mw.SetVisible(true)
}

// setupLogging направляет log в gui.log и в кольцевой буфер (для отправки
// диагностики на сигналку через SendDiagnostics).
func setupLogging() *logbuf.Buffer {
	buf := logbuf.New(200)
	dir, err := os.UserConfigDir()
	if err != nil {
		log.SetOutput(buf)
		return buf
	}
	logDir := filepath.Join(dir, "lanmesh")
	os.MkdirAll(logDir, 0700)
	f, err := os.OpenFile(filepath.Join(logDir, "gui.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.SetOutput(buf)
		return buf
	}
	log.SetOutput(io.MultiWriter(f, buf))
	return buf
}

// --- модели таблиц ----------------------------------------------------------

type netModel struct {
	walk.TableModelBase
	items []app.NetworkView
}

func (m *netModel) RowCount() int { return len(m.items) }
func (m *netModel) Value(row, col int) interface{} {
	n := m.items[row]
	switch col {
	case 0:
		dot := "🟢 "
		if n.SignalError != "" {
			dot = "🟡 "
		}
		return dot + n.Name
	case 1:
		up := 0
		for _, s := range n.Signals {
			if s.Up {
				up++
			}
		}
		return fmt.Sprintf("%d/%d", up, len(n.Signals))
	case 2:
		return len(n.Peers)
	}
	return ""
}

type memModel struct {
	walk.TableModelBase
	items []peer.PeerView
}

func (m *memModel) RowCount() int { return len(m.items) }
func (m *memModel) Value(row, col int) interface{} {
	p := m.items[row]
	switch col {
	case 0:
		return p.Name
	case 1:
		return p.VirtualIP
	case 2:
		switch p.Status {
		case "direct":
			return "🟢 прямое"
		case "relay":
			return "🔵 ретранслятор"
		default:
			return "🟡 подключение"
		}
	case 3:
		if p.RttMs >= 0 {
			return fmt.Sprintf("%.0f мс", p.RttMs)
		}
		return "—"
	}
	return ""
}

// --- обновление ------------------------------------------------------------

func refresh() {
	st := sess.State()

	if !st.Running {
		headerLbl.SetText("Узел не поднят — добавь сеть, чтобы подключиться.")
		statusLbl.SetText("")
	} else {
		headerLbl.SetText(fmt.Sprintf("Я: %s      IP: %s", st.SelfName, st.SelfIP))
		ext := st.SelfEndpoint
		if ext == "" {
			ext = "не определён (STUN молчит — извне не пробьют)"
		}
		statusLbl.SetText("Внешний адрес: " + ext)
	}

	nets := append([]app.NetworkView(nil), st.Networks...)
	sort.Slice(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })

	// Сохраняем выбор по тегу сети, чтобы после перечитывания не прыгал.
	selTag := selectedNetTag()
	netMdl.items = nets
	netMdl.PublishRowsReset()
	if selTag != "" {
		for i, n := range nets {
			if n.Tag == selTag {
				netTV.SetCurrentIndex(i)
				break
			}
		}
	} else if len(nets) > 0 && netTV.CurrentIndex() < 0 {
		netTV.SetCurrentIndex(0)
	}
	refreshMembers()
}

func refreshMembers() {
	i := netTV.CurrentIndex()
	if i < 0 || i >= len(netMdl.items) {
		memMdl.items = nil
	} else {
		memMdl.items = netMdl.items[i].Peers
	}
	memMdl.PublishRowsReset()
}

func selectedNetTag() string {
	i := netTV.CurrentIndex()
	if i < 0 || i >= len(netMdl.items) {
		return ""
	}
	return netMdl.items[i].Tag
}

func selectedNet() (app.NetworkView, bool) {
	i := netTV.CurrentIndex()
	if i < 0 || i >= len(netMdl.items) {
		return app.NetworkView{}, false
	}
	return netMdl.items[i], true
}

// --- действия ---------------------------------------------------------------

func onAddNetwork() {
	name, pass, ok := promptAddNetwork()
	if !ok {
		return
	}
	if err := sess.AddNetwork(name, pass); err != nil {
		walk.MsgBox(mw, "lanmesh", "Не удалось: "+err.Error(), walk.MsgBoxIconError)
		return
	}
	cfgMu.Lock()
	found := false
	for i := range cfg.Networks {
		if cfg.Networks[i].Name == name {
			cfg.Networks[i].Password = pass
			found = true
		}
	}
	if !found {
		cfg.Networks = append(cfg.Networks, netProfile{Name: name, Password: pass})
	}
	cfgMu.Unlock()
	saveConfig()
	refresh()
}

func onLeave() {
	n, ok := selectedNet()
	if !ok {
		walk.MsgBox(mw, "lanmesh", "Выбери сеть в списке слева.", walk.MsgBoxIconInformation)
		return
	}
	if walk.MsgBox(mw, "Выйти из сети", "Выйти из сети «"+n.Name+"»?", walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	if raw, err := hex.DecodeString(n.Tag); err == nil && len(raw) == 32 {
		var tagB [32]byte
		copy(tagB[:], raw)
		sess.RemoveNetwork(tagB)
	}
	cfgMu.Lock()
	out := cfg.Networks[:0]
	for _, p := range cfg.Networks {
		if netTag(p.Name, p.Password) != n.Tag {
			out = append(out, p)
		}
	}
	cfg.Networks = out
	cfgMu.Unlock()
	saveConfig()
	refresh()
}

func onInvite() {
	n, ok := selectedNet()
	if !ok {
		walk.MsgBox(mw, "lanmesh", "Выбери сеть в списке слева.", walk.MsgBoxIconInformation)
		return
	}
	cfgMu.Lock()
	var pass string
	for _, p := range cfg.Networks {
		if netTag(p.Name, p.Password) == n.Tag {
			pass = p.Password
			break
		}
	}
	cfgMu.Unlock()
	if pass == "" {
		walk.MsgBox(mw, "lanmesh", "Пароль сети не найден в конфиге.", walk.MsgBoxIconWarning)
		return
	}
	link := "lanmesh://join?net=" + url.QueryEscape(n.Name) + "&pass=" + url.QueryEscape(pass)
	if err := walk.Clipboard().SetText(link); err == nil {
		walk.MsgBox(mw, "Приглашение", "Ссылка-приглашение скопирована в буфер:\n\n"+link, walk.MsgBoxIconInformation)
	}
}

func onCopyPeerIP() {
	i := memTV.CurrentIndex()
	if i < 0 || i >= len(memMdl.items) {
		return
	}
	walk.Clipboard().SetText(memMdl.items[i].VirtualIP)
}

func onDiagnose() {
	go func() {
		tag, err := sess.SendDiagnostics(context.Background())
		mw.Synchronize(func() {
			if err != nil {
				walk.MsgBox(mw, "Диагностика", "Не удалось: "+err.Error(), walk.MsgBoxIconError)
				return
			}
			walk.MsgBox(mw, "Диагностика", "Отправлено. Код для чтения логов:\n\n"+tag, walk.MsgBoxIconInformation)
		})
	}()
}

// --- права администратора ---------------------------------------------------

func ensureAdmin() {
	if windows.GetCurrentProcessToken().IsElevated() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	cwd, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	if err := windows.ShellExecute(0, verb, file, nil, cwd, 1); err != nil {
		return
	}
	os.Exit(0)
}
