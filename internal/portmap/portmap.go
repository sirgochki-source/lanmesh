// Package portmap просит роутер пробросить UDP-порт: PCP -> NAT-PMP -> UPnP-IGD,
// первый ответивший выигрывает.
//
// Порядок не случаен: PCP (RFC 6887) — единственный, который в принципе может
// работать сквозь операторский CGN; NAT-PMP проще и быстрее; UPnP самый
// распространённый, но самый тяжёлый (SSDP + SOAP + XML).
//
// Зачем это вообще: проброшенный вход endpoint-independent, то есть принимает
// пакет от ЛЮБОГО источника. Это расшивает тупик «port-restricted cone ↔
// симметричный CGNAT», который сегодня лечится только ретранслятором.
package portmap

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"
)

// cascadeTimeout — общий бюджет одного раунда каскада (первичная попытка и
// каждое обновление аренды). Три протокола опрашиваются ПАРАЛЛЕЛЬНО именно
// поэтому: последовательный опрос стоил бы 3×cascadeTimeout на роутере, который
// не умеет ни одного протокола, а Run зовётся на старте узла — трёхкратный ценник
// там особенно чувствителен.
const cascadeTimeout = 3 * time.Second

// mappingLease — запрашиваемая аренда маппинга. 2 часа — общепринятое значение
// среди клиентов NAT-PMP/PCP/UPnP (Apple, miniupnpc): достаточно редко, чтобы не
// дёргать роутер, и достаточно коротко, чтобы висящий маппинг сам исчез, если
// lanmesh упал без штатного выхода (снятие через remove()).
const mappingLease = 2 * time.Hour

// natpmpPort — порт демона NAT-PMP/PCP на роутере. Общий для обоих протоколов
// не случайно: PCP намеренно переиспользует порт NAT-PMP (RFC 6887 §4), чтобы
// PCP-запрос к NAT-PMP-серверу получил осмысленный отлуп, а не тишину.
const natpmpPort = 5351

// cgnat — 100.64.0.0/10, адреса операторского NAT (RFC 6598).
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Usable — можно ли анонсировать этот внешний адрес.
//
// Отбраковка обязательна: роутер за операторским NAT честно отдаст свой WAN-адрес
// из 100.64/10, а при двойном NAT — приватный. Анонс такого адреса сделает ХУЖЕ,
// чем отсутствие проброса: пиры будут долбиться в мусор вместо рабочих кандидатов.
//
// stunIP пустой = STUN промолчал; тогда проброшенный адрес единственный, и сверять
// его не с чем — достаточно, чтобы он был публичным.
func Usable(ext, stunIP netip.Addr) bool {
	if !ext.IsValid() || ext.IsLoopback() || ext.IsPrivate() ||
		ext.IsLinkLocalUnicast() || ext.IsUnspecified() || cgnat.Contains(ext) {
		return false
	}
	if !stunIP.IsValid() {
		return true
	}
	// IP обязан совпасть со STUN. Расхождение означает, что мы видим не тот NAT,
	// который нас реально выпускает наружу, — двойной NAT.
	return ext == stunIP
}

// Mapping — проброшенный внешний адрес и протокол, который его добыл.
type Mapping struct {
	External netip.AddrPort
	Proto    string // "pcp" | "natpmp" | "upnp"
}

// mapper — общий контракт протокола проброса, за которым каскад в Run не видит
// разницы между PCP/NAT-PMP/UPnP.
//
// ctx, передаваемый в add/remove, ОБЯЗАН иметь дедлайн или быть отменяемым —
// реализации закрывают свой сокет по ctx.Done() (см. closeOnDone), это
// единственный способ прервать блокирующее чтение UDP-ответа.
type mapper interface {
	// name — протокол для Mapping.Proto.
	name() string
	// add просит роутер создать/обновить маппинг localPort и возвращает внешний
	// адрес с арендой (0, если протокол не сообщает срок явно — тогда каскад
	// подставляет mappingLease). Повторный вызов на том же mapper — это и есть
	// обновление аренды: PCP/NAT-PMP/UPnP трактуют повторный запрос с теми же
	// параметрами как продление уже созданного маппинга, отдельного метода
	// "renew" не нужно.
	add(ctx context.Context, localPort int) (netip.AddrPort, time.Duration, error)
	// remove снимает маппинг при выходе, чтобы не засорять таблицу роутера
	// висящим правилом. Ошибки не возвращает: это best-effort уборка на выходе,
	// сообщать о её неудаче некому и незачем.
	remove(ctx context.Context, localPort int)
}

// Run держит проброс UDP-порта localPort живым, пока не отменят ctx, и снимает
// его при выходе. stunIP — внешний адрес по данным STUN (netip.Addr{}, если STUN
// не ответил), нужен Usable для отбраковки двойного NAT.
//
// В канал попадает только ПРИГОДНЫЙ (Usable) маппинг — непригодный наружу не
// отдаётся вовсе, чтобы вызывающему не пришлось повторять проверку у себя же и
// эта логика не разъехалась по двум местам.
func Run(ctx context.Context, localPort int, stunIP netip.Addr) <-chan Mapping {
	out := make(chan Mapping)
	go runCascade(ctx, localPort, stunIP, out)
	return out
}

// attempt — результат одной попытки add() внутри каскада.
type attempt struct {
	m     mapper
	ext   netip.AddrPort
	lease time.Duration
	err   error
}

func runCascade(ctx context.Context, localPort int, stunIP netip.Addr, out chan<- Mapping) {
	defer close(out)

	var mappers []mapper
	if gw, err := defaultGateway(); err == nil {
		// PCP и NAT-PMP говорят с шлюзом напрямую — им нужен его адрес.
		mappers = append(mappers, &pcpMapper{gateway: gw}, &natpmpMapper{gateway: gw})
	}
	// UPnP находит роутера сам через SSDP-мультикаст и адрес шлюза не знает —
	// поэтому пробуем его, даже если GetBestRoute не дал ответа (например, узел
	// изолирован от интернета, но локальный роутер с UPnP всё равно рядом).
	mappers = append(mappers, &upnpMapper{})

	tryCtx, cancel := context.WithTimeout(ctx, cascadeTimeout)
	results := make(chan attempt, len(mappers))
	for _, m := range mappers {
		go func(m mapper) {
			ext, lease, err := m.add(tryCtx, localPort)
			results <- attempt{m: m, ext: ext, lease: lease, err: err}
		}(m)
	}

	var winner *attempt
	pending := len(mappers)
	for pending > 0 {
		select {
		case r := <-results:
			pending--
			if r.err != nil {
				continue
			}
			if winner == nil && Usable(r.ext.Addr(), stunIP) {
				w := r
				winner = &w
				select {
				case out <- Mapping{External: r.ext, Proto: r.m.name()}:
				case <-ctx.Done():
				}
			}
			// Иначе результат просто игнорируется — победитель уже есть, либо
			// этот непригоден (CGNAT/двойной NAT). Проактивно снимать его же
			// маппингом НЕ пытаемся: PCP и NAT-PMP на многих роутерах (напр.
			// miniupnpd) делят одну и ту же таблицу перенаправлений, и delete по
			// internal-порту от проигравшего протокола рискует снять запись,
			// от которой зависит уже отданный победитель. Лишняя запись сама
			// истечёт по аренде (mappingLease) — это дешевле риска погасить
			// рабочий маппинг.
		case <-tryCtx.Done():
			pending = 0
		}
	}
	cancel()

	if winner == nil {
		return // никто не ответил пригодным адресом
	}

	keepAlive(ctx, winner, localPort, stunIP, out)
}

// keepAlive обновляет аренду победившего маппинга на половине её срока и снимает
// маппинг при отмене ctx.
func keepAlive(ctx context.Context, winner *attempt, localPort int, stunIP netip.Addr, out chan<- Mapping) {
	lease := winner.lease
	if lease <= 0 {
		lease = mappingLease // протокол не сообщил срок явно (типично для UPnP)
	}
	ticker := time.NewTicker(lease / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			removeCtx, cancel := context.WithTimeout(context.Background(), cascadeTimeout)
			winner.m.remove(removeCtx, localPort)
			cancel()
			return

		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, cascadeTimeout)
			ext, newLease, err := winner.m.add(renewCtx, localPort)
			cancel()
			if err != nil || !Usable(ext.Addr(), stunIP) {
				// Обновление не удалось (или роутер вдруг отдал непригодный
				// адрес) — не убиваем горутину сразу: прежняя аренда ещё не
				// истекла, попробуем на следующем тике.
				continue
			}
			if newLease > 0 {
				lease = newLease
				ticker.Reset(lease / 2)
			}
			if ext != winner.ext {
				winner.ext = ext
				select {
				case out <- Mapping{External: ext, Proto: winner.m.name()}:
				case <-ctx.Done():
				}
			}
		}
	}
}

// closeOnDone закрывает conn при отмене/истечении ctx, чтобы прервать
// блокирующий Read немедленно, а не по истечении отдельного таймера сокета.
// Возвращает stop — его нужно вызвать (обычно defer'ом) до выхода из функции,
// иначе фоновая горутина проживёт до отмены ctx впустую, но не дольше.
func closeOnDone(ctx context.Context, conn io.Closer) (stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

// dialGateway открывает UDP-сокет, "подключённый" к шлюзу: OS-фильтрация по
// адресу/порту избавляет от ручной сверки источника, как это приходится делать
// signal/stun.go на общем сокете.
func dialGateway(gw netip.Addr, port int) (*net.UDPConn, error) {
	return net.DialUDP("udp4", nil, &net.UDPAddr{IP: gw.AsSlice(), Port: port})
}

// sendRecv шлёт req на conn и ждёт один ответ. Повторной отправки нет — как и в
// signal/stun.go: единственная попытка на раунд, а параллельный опрос трёх
// протоколов уже даёт запас на единичную потерю пакета (не удался этот — раунд
// каскада всё равно может закрыть другой протокол).
func sendRecv(conn *net.UDPConn, req, buf []byte) (int, error) {
	if _, err := conn.Write(req); err != nil {
		return 0, err
	}
	return conn.Read(buf)
}

// localAddrOf — локальный IP уже открытого conn (тот, что ОС выбрала для пути к
// его адресату). Unmap() — тот же приём нормализации, что и на боевом сокете
// lanmesh: LocalAddr() на udp4-сокете отдаёт чистый v4, но не полагаемся на это
// молча.
func localAddrOf(conn *net.UDPConn) (netip.Addr, bool) {
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	ip, ok := netip.AddrFromSlice(la.IP)
	if !ok {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// localAddrToward — какой локальный IP ОС выберет для исходящих пакетов к dst.
// UDP Dial ничего не отправляет в сеть (только выбор маршрута ядром), поэтому
// это дёшево и не требует реального сокета к получателю. Нужен UPnP: SOAP идёт
// через net/http, и там нет прямого доступа к LocalAddr() использованного
// сокета, а роутеру для AddPortMapping обязательно назвать НАШ LAN-адрес
// (NewInternalClient) — иначе он перешлёт порт не туда.
func localAddrToward(dst netip.Addr) (netip.Addr, error) {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: dst.AsSlice(), Port: 1})
	if err != nil {
		return netip.Addr{}, err
	}
	defer conn.Close()
	ip, ok := localAddrOf(conn)
	if !ok {
		return netip.Addr{}, errors.New("portmap: не удалось определить локальный адрес")
	}
	return ip, nil
}

// Определение шлюза — единственный системный вызов пакета, вынесен в
// gateway_windows.go: там же живёт вся платформо-зависимость (iphlpapi), а этот
// файл остаётся переносимым, как pcp/natpmp/upnp.
