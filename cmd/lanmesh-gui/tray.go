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
	"time"

	"fyne.io/systray"

	"github.com/sirgochki-source/lanmesh/internal/peer"
)

// maxPeerSlots — сколько строк участников показываем прямо в меню трея. systray
// не умеет добавлять/удалять пункты на лету, поэтому слоты создаём заранее и
// прячем лишние.
const maxPeerSlots = 8

// Иконки статуса — цветной кружок. Генерим в рантайме (см. dotICO), чтобы не
// таскать бинарные ассеты. Цвета совпадают с палитрой панели (--muted/--warn/--ok).
var (
	iconOff  = dotICO(0x8b, 0x95, 0xa5) // серый — не подключено
	iconWarn = dotICO(0xe9, 0xb9, 0x49) // жёлтый — ограничено
	iconOn   = dotICO(0x35, 0xc6, 0x6b) // зелёный — в сети
)

// runTray показывает иконку в трее с живым статусом: цвет иконки и текст меню
// обновляются раз в 2с из состояния сеанса. Блокируется до выхода (systray.Quit).
func runTray(panelURL string) {
	systray.Run(func() {
		systray.SetIcon(iconOff)
		systray.SetTooltip("lanmesh")

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

		mConnect := systray.AddMenuItem("Подключиться", "Подключиться к сохранённой сети")
		mDisconnect := systray.AddMenuItem("Отключиться", "Снять сеть")
		systray.AddSeparator()
		mOpen := systray.AddMenuItem("Открыть панель", "Показать участников сети")
		mQuit := systray.AddMenuItem("Выход", "Отключиться и закрыть")

		go func() {
			for {
				select {
				case <-mConnect.ClickedCh:
					cfgMu.Lock()
					net, pass := cfg.Network, cfg.Password
					cfgMu.Unlock()
					if net != "" && pass != "" {
						// Start блокирует до готовности адаптера — не держим цикл кликов.
						go func() {
							if err := sess.Start(net, pass); err != nil {
								log.Printf("трей: подключение: %v", err)
							}
						}()
					}
				case <-mDisconnect.ClickedCh:
					go sess.Stop()
				case <-mOpen.ClickedCh:
					openBrowser(panelURL)
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()

		// Живой статус: красим иконку и переписываем меню под текущее состояние.
		go func() {
			updateTray(mStatus, mPeers, mConnect, mDisconnect)
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for range t.C {
				updateTray(mStatus, mPeers, mConnect, mDisconnect)
			}
		}()

		log.Printf("трей запущен")
	}, func() {
		log.Printf("трей закрыт")
	})
}

// updateTray подгоняет иконку и пункты меню под текущий снимок сеанса.
func updateTray(mStatus *systray.MenuItem, mPeers []*systray.MenuItem, mConnect, mDisconnect *systray.MenuItem) {
	st := sess.State()

	// «Ограничено» — та же честная формула, что в панели: адаптер поднят, но
	// либо нет внешнего адреса (нас не пробьют), либо молчит сигналка.
	degraded := st.Running && (st.SelfEndpoint == "" || st.SignalError != "")
	switch {
	case !st.Running:
		systray.SetIcon(iconOff)
		systray.SetTooltip("lanmesh — не подключено")
		mStatus.SetTitle("не подключено")
	case degraded:
		systray.SetIcon(iconWarn)
		systray.SetTooltip("lanmesh — " + st.Network + " (ограничено)")
		mStatus.SetTitle(st.Network + " — ограничено")
	default:
		systray.SetIcon(iconOn)
		systray.SetTooltip(fmt.Sprintf("lanmesh — %s (%d в сети)", st.Network, len(st.Peers)))
		mStatus.SetTitle(fmt.Sprintf("%s — %d в сети", st.Network, len(st.Peers)))
	}

	for i, mp := range mPeers {
		if i < len(st.Peers) {
			mp.SetTitle(peerLine(st.Peers[i]))
			mp.Show()
		} else {
			mp.Hide()
		}
	}

	cfgMu.Lock()
	net := cfg.Network
	haveCreds := cfg.Network != "" && cfg.Password != ""
	cfgMu.Unlock()

	if st.Running {
		mConnect.SetTitle("Подключиться")
		mConnect.Disable()
		mDisconnect.Enable()
	} else {
		mDisconnect.Disable()
		if haveCreds {
			mConnect.SetTitle("Подключиться к " + net)
			mConnect.Enable()
		} else {
			// Пароль не сохранён — из трея вслепую не подключишься.
			mConnect.SetTitle("Подключиться (открой панель)")
			mConnect.Disable()
		}
	}
}

// peerLine — строка участника для меню трея. Без эмодзи: Windows их плохо рисует
// в контекстном меню, путь пишем словом.
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

// dotICO рисует залитый кружок заданного цвета и заворачивает его в .ico
// (PNG внутри ICO — Windows Vista+ это понимает). Возвращает готовые байты
// иконки для systray.SetIcon.
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
	// ICONDIR
	binary.Write(&ico, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&ico, binary.LittleEndian, uint16(1)) // type = icon
	binary.Write(&ico, binary.LittleEndian, uint16(1)) // число картинок
	// ICONDIRENTRY
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
