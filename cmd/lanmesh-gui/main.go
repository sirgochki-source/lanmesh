//go:build windows

// Command lanmesh-gui — графическая оболочка над сеансом lanmesh.
//
// Двойной клик по exe: приложение поднимается с правами администратора (нужно
// для сетевого адаптера), открывает в браузере локальную панель управления и
// живёт в системном трее. Панель показывает сеть и её участников и даёт
// подключаться/отключаться без командной строки.
package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"unsafe"

	"fyne.io/systray"
	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/sirgochki-source/lanmesh/internal/app"
	"github.com/sirgochki-source/lanmesh/internal/logbuf"
	"github.com/sirgochki-source/lanmesh/internal/panel"
)

const ifaceName = "lanmesh"

// listenAddr — адрес панели. Хост и порт задаёт сам пакет panel: наружу панель
// не выставляется принципиально (аутентификации у неё нет), поэтому настраивать
// тут нечего.
var listenAddr = fmt.Sprintf("%s:%d", panel.Host, panel.DefaultPort)

var (
	// pnl — панель, которой владеет это окно. Нужна хендлерам автообновления: они
	// Windows-only и живут здесь, а не в переносимом пакете.
	pnl *panel.Panel
	// sess — сеанс под панелью; трей и выход из окна дёргают его напрямую.
	sess *app.Session
)


func main() {
	// Единственный экземпляр: если панель уже слушает 8737 — другой экземпляр уже
	// запущен (возможно, свёрнут в трей). Показываем его окно и выходим, а не падаем
	// молча. Порт не требует прав, поэтому проверяем ДО UAC — повторный клик по exe
	// не плодит UAC-запрос и не трогает сетевой адаптер.
	if probe, err := net.Listen("tcp", listenAddr); err != nil {
		showExisting()
		return
	} else {
		probe.Close()
	}

	logs := setupLogging()
	cleanupOldExe() // убрать .old/.new-хвосты прошлого автообновления

	// Адаптер требует прав администратора — если их нет, перезапускаемся с UAC.
	ensureAdmin()

	// Сеанс и панель поверх него — сборка общая с Linux-клиентом, см.
	// internal/panel. Здесь остаётся только оконная обвязка.
	sess, pnl = panel.Start(panel.Options{Version: version, CanUpdate: true, Logs: logs, Iface: ifaceName})

	// Сокет открываем заранее, чтобы браузер стартовал только на готовый сервер.
	// Адрес спрашиваем у панели, а не берём listenAddr: тот нужен раньше — для
	// проверки единственного экземпляра, до того как панель создана.
	ln, err := net.Listen("tcp", pnl.Addr())
	if err != nil {
		log.Fatalf("panel listen: %v", err)
	}

	mux := http.NewServeMux()
	pnl.Routes(mux)
	// Автообновление тянет .exe и существует только под Windows, поэтому его
	// маршруты регистрирует окно, а не переносимый пакет панели. Признак
	// CanUpdate выше говорит фронту, что кнопку рисовать можно.
	mux.HandleFunc("/api/checkupdate", pnl.Guard(handleCheckUpdate))
	mux.HandleFunc("/api/update", pnl.Guard(handleUpdate))

	go func() {
		if err := http.Serve(ln, mux); err != nil {
			log.Fatalf("panel serve: %v", err)
		}
	}()

	// Нативное окно (WebView2) на локальную панель — тот же UI, но не в браузере,
	// без вкладок и адресной строки. Держит главный поток до закрытия окна.
	// Размеры — в физических пикселях (процесс per-monitor-DPI-aware, см. init),
	// поэтому домножаем на масштаб первичного монитора: без этого окно на 125/150%
	// открылось бы визуально мелким.
	initScale := dpiScaleSystem()
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug: false,
		WindowOptions: webview2.WindowOptions{
			Title:  "lanmesh",
			Width:  uint(scalePx(980, initScale)),
			Height: uint(scalePx(660, initScale)),
			Center: true,
		},
	})
	if w == nil {
		log.Fatal("не удалось создать окно (нужен WebView2 Runtime — component из Microsoft Edge)")
	}
	defer w.Destroy()
	// Все размеры окна — физические пиксели (per-monitor-aware), домножаем на масштаб
	// текущего монитора окна. Масштаб берём в момент вызова, а не один раз: окно могут
	// перетащить на монитор с другим DPI между сменами режима.
	winScale := dpiScaleForWindow(uintptr(w.Window()))
	w.SetSize(scalePx(360, winScale), scalePx(480, winScale), webview2.HintMin)
	// Мост JS→native: кнопка ⤢/⤡ в панели меняет размер нативного окна под режим
	// (компакт узкое / подробный широкое). Размер меняем на UI-потоке через Dispatch.
	w.Bind("lmResize", func(mode string) {
		w.Dispatch(func() {
			s := dpiScaleForWindow(uintptr(w.Window()))
			if mode == "detailed" {
				w.SetSize(scalePx(980, s), scalePx(660, s), webview2.HintNone)
			} else {
				w.SetSize(scalePx(460, s), scalePx(720, s), webview2.HintNone)
			}
		})
	})
	// Кнопки своей полосы-заголовка: свернуть / закрыть (закрытие прячет в трей).
	w.Bind("lmWindow", func(action string) {
		hwnd := uintptr(w.Window())
		w.Dispatch(func() {
			switch action {
			case "minimize":
				procShowWindow.Call(hwnd, swMinimize)
			case "close":
				procShowWindow.Call(hwnd, swHide)
			}
		})
	})
	// Мост перетаскивания окна: фронтенд зовёт lmDrag на mousedown по своей полосе-
	// заголовку (WM_NCHITTEST не работает — WebView2 накрывает клиент, см. dragWindow).
	w.Bind("lmDrag", func() {
		hwnd := uintptr(w.Window())
		w.Dispatch(func() { dragWindow(hwnd) })
	})
	// Окно «своё», без нативной рамки Windows: сперва ставим субклассинг (перехват
	// WM_NCCALCSIZE/WM_CLOSE), затем makeFrameless убирает нативный заголовок и
	// пересчитывает рамку (SWP_FRAMECHANGED) — уже через наш перехватчик, поэтому
	// верхняя кромка убирается сразу. Полосу-заголовок рисует приложение; ресайз —
	// нативной рамкой по краям; перетаскивание — через мост lmDrag.
	installFrame(uintptr(w.Window()))
	makeFrameless(uintptr(w.Window()))
	setWindowIcon(uintptr(w.Window())) // иконка окна на таскбаре/Alt-Tab из ресурса exe

	// Иконка в системном трее + меню (Открыть окно / Выход) — на отдельном
	// залоченном OS-потоке, чтобы её message-loop не конфликтовал с главным
	// message-loop WebView2 (w.Run() держит главный поток до закрытия окна).
	startTray(w)

	w.Navigate("http://" + listenAddr)
	w.Run()
	systray.Quit() // окно закрыли напрямую — гасим трей, чтобы процесс завершился
	sess.Stop()
}

// --- системный трей и окно -------------------------------------------------

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProc   = user32.NewProc("CallWindowProcW")
	procFindWindow       = user32.NewProc("FindWindowW")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procSendMessage      = user32.NewProc("SendMessageW")
	procReleaseCapture   = user32.NewProc("ReleaseCapture")
	procExtractIconEx    = windows.NewLazySystemDLL("shell32.dll").NewProc("ExtractIconExW")

	// DPI: объявляем осведомлённость процесса и читаем масштаб монитора. Win10 1607+
	// (GetDpiFor*) / 1703+ (SetProcessDpiAwarenessContext); WebView2 требует Win10, так
	// что на боевых машинах функции всегда есть. На старее — .Find() != nil, no-op.
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
)

// dpiPerMonitorAwareV2 — DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (значение -4,
// передаётся как псевдодескриптор). Окно само отслеживает смену DPI при переносе
// на другой монитор и перерисовывается чётко.
const dpiPerMonitorAwareV2 = ^uintptr(3) // -4

// init объявляет процесс per-monitor-v2 DPI-aware ДО создания любого окна (пакетные
// init выполняются раньше main). Без этого Windows считает процесс DPI-unaware и на
// мониторах с масштабом >100% растягивает окно WebView2 битмапом — интерфейс у
// пользователей со 125/150% становится мыльным. Манифест Go-линкера DPI не объявляет,
// поэтому ставим осведомлённость в рантайме. Осведомлённость раньше никто не трогает,
// так что вызов проходит (иначе Windows вернул бы ERROR_ACCESS_DENIED).
func init() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		procSetProcessDpiAwarenessContext.Call(dpiPerMonitorAwareV2)
	}
}

// dpiScaleForWindow — множитель размеров относительно 96 DPI (1.0 = 100%) для того
// монитора, на котором сейчас окно. В per-monitor-aware процессе go-webview2 отдаёт
// размеры в CreateWindow/SetWindowPos как физические пиксели, поэтому все хардкод-
// размеры окна домножаем на этот множитель, иначе окно выходит визуально мелким.
func dpiScaleForWindow(hwnd uintptr) float64 {
	if procGetDpiForWindow.Find() != nil {
		return 1
	}
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// dpiScaleSystem — то же для первичного монитора; нужно для стартового размера окна,
// когда hwnd ещё не создан (центрирование в go-webview2 тоже идёт по физическим
// SM_CXSCREEN/SM_CYSCREEN, поэтому масштаб стартового размера центр не сбивает).
func dpiScaleSystem() float64 {
	if procGetDpiForSystem.Find() != nil {
		return 1
	}
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// scalePx округляет размер px, домноженный на DPI-масштаб scale.
func scalePx(px int, scale float64) int { return int(float64(px)*scale + 0.5) }

// setWindowIcon ставит окну иконку из ресурсов самого exe (её же видит проводник) —
// иначе окно WebView2 не подхватывает её, и на таскбаре/в Alt-Tab пусто. ExtractIconEx
// достаёт первую иконку exe по индексу 0 (не нужно знать resource ID).
func setWindowIcon(hwnd uintptr) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	p, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return
	}
	var big, small uintptr
	procExtractIconEx.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1)
	const wmSetIcon = 0x0080
	if big != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, 1, big) // ICON_BIG
	}
	if small != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, 0, small) // ICON_SMALL
	}
}

// showExisting поднимает окно уже запущенного экземпляра lanmesh (по заголовку) —
// на случай, когда его окно свёрнуто в трей, а пользователь снова кликнул по exe.
func showExisting() {
	title, err := windows.UTF16PtrFromString("lanmesh")
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd != 0 {
		procShowWindow.Call(hwnd, swRestore)
		procSetForeground.Call(hwnd)
	}
}

// Своя (frameless) рамка окна. Подход устойчив к тому, что дочернее окно WebView2
// накрывает всю клиентскую область (из-за чего WM_NCHITTEST у родителя не срабатывал,
// и полоса не перетаскивалась):
//   - убираем WS_CAPTION (нативный заголовок), но ОСТАВЛЯЕМ WS_THICKFRAME — тонкую
//     рамку ресайза: за края окно тянется нативно (рамка — не-клиентская зона,
//     WebView2 её не накрывает);
//   - полосу-заголовок рисует само приложение; перетаскивание инициирует фронтенд
//     через мост lmDrag (dragWindow) — ReleaseCapture + WM_NCLBUTTONDOWN(HTCAPTION);
//   - WM_CLOSE (наш крестик из панели) прячет окно в трей, а не уничтожает.
const (
	swMinimize   = 6      // SW_MINIMIZE
	swHide       = 0      // SW_HIDE — спрятать окно (сворачивание в трей)
	swRestore    = 9      // SW_RESTORE — восстановить/показать окно
	wmClose      = 0x0010 // WM_CLOSE
	wmNCCalcSize = 0x0083 // WM_NCCALCSIZE

	gwlStyle  = -16        // GWL_STYLE
	wsCaption = 0x00C00000 // WS_CAPTION — нативный заголовок, снимаем

	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020 // пересчитать рамку СРАЗУ, не дожидаясь сворачивания

	wmNCLButtonDown = 0x00A1 // WM_NCLBUTTONDOWN
	htCaption       = 2      // HTCAPTION — «нажали на заголовок» → нативный цикл перемещения
)

// origWndProc — оригинальная оконная процедура WebView2, сохранённая при установке
// нашего перехватчика (installFrame). Ненулевой ⇒ перехват стоит.
var origWndProc uintptr

type winRect struct{ left, top, right, bottom int32 }

// nccalcsizeParams — NCCALCSIZE_PARAMS: при wParam=TRUE lParam указывает на неё,
// rgrc[0] на входе = предлагаемый прямоугольник окна, на выходе = клиентский.
type nccalcsizeParams struct {
	rgrc  [3]winRect
	lppos uintptr
}

// makeFrameless убирает нативный заголовок (WS_CAPTION), оставляя рамку ресайза.
// SWP_FRAMECHANGED заставляет Windows пересчитать не-клиентскую область и клиент
// НЕМЕДЛЕННО — иначе смена стиля проявляется только после сворачивания/разворачивания.
func makeFrameless(hwnd uintptr) {
	gwl := int32(gwlStyle) // через переменную: uintptr(константа -16) переполняется при компиляции
	style, _, _ := procGetWindowLongPtr.Call(hwnd, uintptr(gwl))
	procSetWindowLongPtr.Call(hwnd, uintptr(gwl), style&^wsCaption)
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
}

// dragWindow запускает нативное перетаскивание окна от текущей позиции курсора —
// вызывается из фронтенда (мост lmDrag) на mousedown по своей полосе-заголовку.
// ReleaseCapture снимает захват мыши, WM_NCLBUTTONDOWN(HTCAPTION) вводит окно в
// системный цикл перемещения (с ним же работает Aero-snap к краям экрана).
func dragWindow(hwnd uintptr) {
	procReleaseCapture.Call()
	procSendMessage.Call(hwnd, wmNCLButtonDown, htCaption, 0)
}

// installFrame субклассирует оконную процедуру WebView2:
//   - WM_NCCALCSIZE: убираем ВЕРХНЮЮ не-клиентскую кромку (иначе без заголовка
//     Windows рисует её светлой — «белая полоса сверху»), сохраняя боковые/нижнюю
//     рамки ресайза. Берём дефолтный расчёт рамки и возвращаем верх клиента к краю окна;
//   - WM_CLOSE: наш крестик (из панели) прячет окно в трей, а не уничтожает его.
//
// Прочие сообщения делегируем оригинальной процедуре.
func installFrame(hwnd uintptr) {
	gwlpWndProc := int32(-4) // GWLP_WNDPROC
	origWndProc, _, _ = procGetWindowLongPtr.Call(hwnd, uintptr(gwlpWndProc))
	newProc := windows.NewCallback(func(h, msg, wparam, lparam uintptr) uintptr {
		switch msg {
		case wmClose:
			procShowWindow.Call(h, swHide)
			return 0 // проглатываем закрытие — окно не уничтожается
		case wmNCCalcSize:
			if wparam != 0 {
				// lparam — указатель на NCCALCSIZE_PARAMS, которым владеет Windows (стек
				// диспетчера сообщений), не объект под GC. `go vet` помечает строку ниже
				// (possible misuse of unsafe.Pointer) — ложное срабатывание: адрес стабилен
				// на время обработки сообщения, перемещения нет.
				sp := (*nccalcsizeParams)(unsafe.Pointer(lparam))
				top := sp.rgrc[0].top                                        // верх окна до расчёта рамки
				procCallWindowProc.Call(origWndProc, h, msg, wparam, lparam) // дефолтная рамка (инсеты со всех сторон)
				sp.rgrc[0].top = top                                         // вернуть верх → клиент до края, без белой полосы
				return 0
			}
		}
		ret, _, _ := procCallWindowProc.Call(origWndProc, h, msg, wparam, lparam)
		return ret
	})
	procSetWindowLongPtr.Call(hwnd, uintptr(gwlpWndProc), newProc)
}

// --- логи, права, браузер ---------------------------------------------------

// setupLogging направляет log в gui.log и параллельно в кольцевой буфер, из
// которого сеанс отправляет диагностику на сигналку. Возвращает этот буфер.
func setupLogging() *logbuf.Buffer {
	buf := logbuf.New(200)

	dir, err := app.ConfigDir()
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
