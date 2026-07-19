//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"runtime"
	"time"

	"fyne.io/systray"
	"github.com/jchv/go-webview2"

	"github.com/sirgochki-source/lanmesh/internal/peer"
)

// maxPeerSlots — сколько строк участников показываем прямо в меню трея. systray не
// умеет добавлять/удалять пункты на лету, поэтому слоты создаём заранее и прячем лишние.
const maxPeerSlots = 8

// Иконки статуса — цветной кружок, генерим в рантайме (dotICO), чтобы не таскать
// бинарные ассеты. Цвета в тон панели: серый/жёлтый/зелёный.
var (
	iconOff  = dotICO(0x8b, 0x95, 0xa5) // серый — не подключено
	iconWarn = dotICO(0xe9, 0xb9, 0x49) // жёлтый — ограничено
	iconOn   = dotICO(0x35, 0xc6, 0x6b) // зелёный — в сети
)

// startTray поднимает иконку lanmesh в системном трее на ОТДЕЛЬНОМ залоченном
// OS-потоке (systray.Run качает своё окно там, не мешая message-loop WebView2 на
// главном потоке). Меню: живой статус, список участников, Подключиться/Отключиться
// (без выхода из сетей), Открыть окно, Выход. Иконка красится по состоянию (тикер 2с).
func startTray(w webview2.WebView) {
	hwnd := uintptr(w.Window()) // HWND панели — стабильный хэндл, не GC-указатель
	go func() {
		runtime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(iconOff)
			systray.SetTitle("lanmesh")
			systray.SetTooltip("lanmesh — виртуальная локалка для игр")

			mStatus := systray.AddMenuItem("не подключено", "")
			mStatus.Disable() // строка-статус, не кнопка
			systray.AddSeparator()

			mPeers := make([]*systray.MenuItem, maxPeerSlots)
			for i := range mPeers {
				mPeers[i] = systray.AddMenuItem("", "")
				mPeers[i].Disable() // информационные строки
				mPeers[i].Hide()
			}
			systray.AddSeparator()

			mConnect := systray.AddMenuItem("Подключиться", "Переподнять сохранённые сети")
			mDisconnect := systray.AddMenuItem("Отключиться", "Уйти в офлайн, не выходя из сетей")
			systray.AddSeparator()
			mOpen := systray.AddMenuItem("Открыть окно", "Показать панель lanmesh")
			mQuit := systray.AddMenuItem("Выход", "Закрыть lanmesh")

			go func() {
				for {
					select {
					case <-mConnect.ClickedCh:
						go trayReconnect() // AddNetwork блокирует до готовности узла — не держим цикл кликов
					case <-mDisconnect.ClickedCh:
						go sess.Stop()
					case <-mOpen.ClickedCh:
						w.Dispatch(func() {
							procShowWindow.Call(hwnd, swRestore)
							procSetForeground.Call(hwnd)
						})
					case <-mQuit.ClickedCh:
						// Terminate на ГЛАВНОМ потоке (см. фикс в main): PostQuitMessage
						// должен уйти в поток w.Run(), иначе процесс не завершится.
						w.Dispatch(func() { w.Terminate() })
						return
					}
				}
			}()

			// Живой статус: раз в 2с красим иконку и переписываем меню под состояние.
			go func() {
				updateTray(mStatus, mPeers, mConnect, mDisconnect)
				t := time.NewTicker(2 * time.Second)
				defer t.Stop()
				for range t.C {
					updateTray(mStatus, mPeers, mConnect, mDisconnect)
				}
			}()
		}, func() {})
	}()
}

// trayReconnect переподнимает все сохранённые сети (как /api/reconnect, но из трея).
func trayReconnect() {
	cfgMu.Lock()
	nets := append([]NetProfile(nil), cfg.Networks...)
	cfgMu.Unlock()
	for _, p := range nets {
		if err := sess.AddNetwork(p.Name, p.Password); err != nil {
			log.Printf("трей: подключение %q: %v", p.Name, err)
		}
	}
}

// updateTray подгоняет иконку и пункты меню под текущий снимок сеанса. Мультисеть:
// статус и список участников — агрегат по всем сетям.
func updateTray(mStatus *systray.MenuItem, mPeers []*systray.MenuItem, mConnect, mDisconnect *systray.MenuItem) {
	st := sess.State()

	var peers []peer.PeerView
	anyBad := false
	for _, n := range st.Networks {
		peers = append(peers, n.Peers...)
		if n.SignalError != "" {
			anyBad = true
		}
	}
	// «Ограничено» — та же честная формула, что в панели: адаптер поднят, но либо нет
	// внешнего адреса (нас не пробьют), либо молчит сигналка.
	degraded := st.Running && (st.SelfEndpoint == "" || anyBad)
	switch {
	case !st.Running:
		systray.SetIcon(iconOff)
		systray.SetTooltip("lanmesh — не подключено")
		mStatus.SetTitle("не подключено")
	case degraded:
		systray.SetIcon(iconWarn)
		systray.SetTooltip("lanmesh — ограничено")
		mStatus.SetTitle("ограничено")
	default:
		systray.SetIcon(iconOn)
		systray.SetTooltip(fmt.Sprintf("lanmesh — %d в сети", len(peers)))
		mStatus.SetTitle(fmt.Sprintf("%d в сети", len(peers)))
	}

	for i, mp := range mPeers {
		if i < len(peers) {
			mp.SetTitle(peerLine(peers[i]))
			mp.Show()
		} else {
			mp.Hide()
		}
	}

	cfgMu.Lock()
	saved := len(cfg.Networks)
	cfgMu.Unlock()

	if st.Running {
		mConnect.SetTitle("Подключиться")
		mConnect.Disable()
		mDisconnect.Enable()
	} else {
		mDisconnect.Disable()
		if saved > 0 {
			mConnect.SetTitle("Подключиться")
			mConnect.Enable()
		} else {
			mConnect.SetTitle("Подключиться (добавь сеть)")
			mConnect.Disable()
		}
	}
}

// peerLine — строка участника для меню трея. Без эмодзи: Windows плохо рисует их в
// контекстном меню, путь пишем словом.
func peerLine(p peer.PeerView) string {
	name := p.Name
	if name == "" {
		name = "узел"
	}
	var path string
	switch p.Status {
	case "direct":
		path = "прямое"
	case "relay":
		path = "ретранслятор"
	default:
		path = "подключение…"
	}
	if p.RttMs >= 0 {
		rtt := fmt.Sprintf("%.0fмс", p.RttMs)
		if p.RttMs < 10 {
			rtt = fmt.Sprintf("%.1fмс", p.RttMs)
		}
		return fmt.Sprintf("  %s · %s · %s", name, rtt, path)
	}
	return fmt.Sprintf("  %s · %s", name, path)
}

// dotICO рисует залитый кружок заданного цвета и заворачивает его в .ico (PNG внутри
// ICO — Windows Vista+ это понимает). Возвращает готовые байты для systray.SetIcon.
func dotICO(r, g, b uint8) []byte {
	const s = 32
	img := image.NewNRGBA(image.Rect(0, 0, s, s))
	cx, cy, rad := float64(s)/2, float64(s)/2, float64(s)/2-2
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := math.Sqrt(dx*dx + dy*dy)
			var a float64
			switch {
			case d <= rad-1:
				a = 1
			case d < rad+1:
				a = (rad + 1 - d) / 2 // мягкий край, чтобы не был лесенкой
			}
			if a > 0 {
				img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: uint8(a * 255)})
			}
		}
	}

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return nil
	}
	p := pngBuf.Bytes()

	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, uint16(0))      // reserved
	binary.Write(&ico, binary.LittleEndian, uint16(1))      // type = icon
	binary.Write(&ico, binary.LittleEndian, uint16(1))      // число картинок
	ico.WriteByte(s)                                        // ширина
	ico.WriteByte(s)                                        // высота
	ico.WriteByte(0)                                        // палитра (0 = truecolor)
	ico.WriteByte(0)                                        // reserved
	binary.Write(&ico, binary.LittleEndian, uint16(1))      // плоскостей
	binary.Write(&ico, binary.LittleEndian, uint16(32))     // бит на пиксель
	binary.Write(&ico, binary.LittleEndian, uint32(len(p))) // размер картинки
	binary.Write(&ico, binary.LittleEndian, uint32(22))     // смещение = 6+16
	ico.Write(p)
	return ico.Bytes()
}
