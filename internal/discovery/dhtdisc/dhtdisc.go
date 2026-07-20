// Package dhtdisc — обнаружение пиров через публичную DHT сети BitTorrent
// (Mainline, BEP 5) вместо своей сигналки.
//
// Идея: DHT — это всемирный распределённый словарь «20-байтный ключ → список
// IP:port», который держат миллионы чужих торрент-клиентов. Ключ не обязан быть
// хэшем торрента — это просто 20 байт. Мы кладём туда хэш СВОЕЙ сети, выведенный
// из сетевого ключа (то есть из имени+пароля):
//
//	announce_peer(инфохэш) — «запиши, что я сижу на этом ключе»
//	get_peers(инфохэш)     — «дай всех, кто на нём сидит»
//
// Обе стороны, зная имя и пароль сети, независимо вычисляют один и тот же
// инфохэш и находят друг друга у посторонних узлов, которых выбрала математика
// (хранят запись те, чьи ID ближе всего к ключу). Ни своего сервера, ни белого
// IP, ни домена для этого не нужно. Через DHT идут ТОЛЬКО адреса — весь трафик
// сети по-прежнему шифрован ключом сети и ходит напрямую между пирами.
//
// Чего DHT не делает: она не пробивает NAT. Найденный адрес дальше долбит движок
// (peer.Engine.AddProbes), и пара за симметричным NAT/CGNAT без ретранслятора не
// соединится — ровно как и с сигналкой.
//
// Приватность: инфохэш ротируется посуточно (HMAC(ключ, дата)), иначе один
// постоянный ключ позволял бы годами следить за составом сети. Ротация НЕ
// защищает от того, кто знает имя и пароль: он вычислит инфохэш на любую дату и
// увидит IP участников. Это цена отказа от собственной инфраструктуры.
package dhtdisc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	alog "github.com/anacrolix/log"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
)

// maxPeersPerRound — потолок адресов, отдаваемых за раунд. В публичной DHT
// анонсировать на наш инфохэш может кто угодно, так что список — это кандидаты,
// а не участники: настоящим пир становится только после расшифровки его кадра
// ключом сети.
const maxPeersPerRound = 64

// roundTimeout — сколько ждём обхода DHT. Холодный старт (bootstrap с нуля)
// занимает около 20с, тёплый — единицы секунд; берём с запасом.
const roundTimeout = 60 * time.Second

// announceGrace — сколько после остановки обхода ждём, чтобы наш announce_peer к
// уже найденным узлам успел уйти в сеть, прежде чем закрыть Announce.
const announceGrace = 5 * time.Second

// Discoverer — узел DHT со своим UDP-сокетом, общий на все сети lanmesh.
// Отдельный сокет, а не боевой: пакеты DHT — открытый bencode от чужих клиентов,
// мешать их с шифрованными кадрами на одном порту значило бы разбирать «чей
// пакет» по первому байту, а он у нас случайный (нонс).
type Discoverer struct {
	srv       *dht.Server
	nodesPath string

	mu       sync.Mutex
	closed   bool
	lastSave time.Time
}

// New поднимает DHT-узел. nodesPath — файл кэша известных узлов DHT (пусто =
// не кэшировать). Кэш важен: с ним вход в сеть занимает секунды и не требует
// bootstrap-узлов, без него каждый старт начинается с них.
func New(nodesPath string) (*Discoverer, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, fmt.Errorf("dht: сокет: %w", err)
	}

	cfg := dht.NewDefaultServerConfig()
	cfg.Conn = conn
	// Библиотека по умолчанию сыплет отладку в стандартный логгер, а он у нас
	// уходит в кольцевой буфер диагностики и на сигналку — забило бы всё.
	cfg.Logger = alog.Default.FilterLevel(alog.Warning)

	srv, err := dht.NewServer(cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("dht: сервер: %w", err)
	}

	d := &Discoverer{srv: srv, nodesPath: nodesPath}
	if nodesPath != "" {
		// Ошибку не считаем фатальной: нет кэша (первый запуск) — войдём через
		// bootstrap-узлы, они для того и нужны.
		if n, err := srv.AddNodesFromFile(nodesPath); err == nil && n > 0 {
			d.lastSave = time.Now()
		}
	}
	return d, nil
}

// Addr — локальный адрес DHT-сокета (для диагностики).
func (d *Discoverer) Addr() net.Addr { return d.srv.Addr() }

// NumNodes — сколько узлов DHT сейчас в таблице маршрутизации. Ноль после
// нескольких раундов означает, что DHT недоступна (режет провайдер/файрвол).
func (d *Discoverer) NumNodes() int { return d.srv.NumNodes() }

// Bootstrap набирает начальную таблицу узлов. Блокируется до готовности или
// отмены; повторный вызов безвреден (библиотека сама решает, нужен ли обход).
func (d *Discoverer) Bootstrap(ctx context.Context) error {
	_, err := d.srv.BootstrapContext(ctx)
	return err
}

// Round — один раунд обнаружения для сети с ключом key: анонсируем себя на порт
// port и собираем адреса остальных. Возвращает найденные "ip:port" без дублей.
//
// port — наш ВНЕШНИЙ порт (тот, что видит интернет у боевого UDP-сокета): именно
// на него пиры будут пробиваться. IP в записи проставит принимающий узел DHT сам,
// из адреса нашего пакета, — подделать его нельзя. port=0 (внешний адрес ещё не
// известен) означает «только искать, себя не анонсировать»: даже так связь
// установится — мы найдём пира, долбанём его первыми, а он узнает нас из
// входящего кадра.
//
// Обрабатываем инфохэши и на сегодня, и на вчера: сутки — граница ротации, а
// часы у всех разные, и на стыке дня стороны иначе разъехались бы по разным
// ключам.
func (d *Discoverer) Round(ctx context.Context, key [crypto.KeySize]byte, port int) ([]string, error) {
	now := time.Now().UTC()
	hashes := [][20]byte{Infohash(key, now), Infohash(key, now.AddDate(0, 0, -1))}

	// Оба инфохэша обходим ПАРАЛЛЕЛЬНО: round1 у каждого может висеть до roundTimeout
	// (медленная/режущая UDP сеть — типично для мобильного оператора), и
	// последовательный вызов удваивал бы такт цикла, растягивая переанонс вдвое
	// именно там, где важнее всего быстро реагировать на смену адреса.
	type res struct {
		found []string
		err   error
	}
	results := make([]res, len(hashes))
	var wg sync.WaitGroup
	for i, ih := range hashes {
		wg.Add(1)
		go func(i int, ih [20]byte) {
			defer wg.Done()
			f, err := d.round1(ctx, ih, port)
			results[i] = res{found: f, err: err}
		}(i, ih)
	}
	wg.Wait()

	seen := make(map[string]bool, maxPeersPerRound)
	out := make([]string, 0, maxPeersPerRound)
	var firstErr error
	for _, r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		for _, s := range r.found {
			if seen[s] || len(out) >= maxPeersPerRound {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	return out, firstErr
}

// round1 — обход по одному инфохэшу.
func (d *Discoverer) round1(ctx context.Context, ih [20]byte, port int) ([]string, error) {
	var opts []dht.AnnounceOpt
	if port > 0 {
		opts = append(opts, dht.AnnouncePeer(dht.AnnouncePeerOpts{Port: port}))
	}
	a, err := d.srv.AnnounceTraversal(ih, opts...)
	if err != nil {
		return nil, err
	}

	// Close() отменяет и наш announce_peer к найденным узлам. Поэтому по завершению
	// поиска (таймаут / канал / набрали хватит) сначала гасим ТОЛЬКО обход
	// (StopTraversing), даём announce-фазе доехать до сокета в пределах announceGrace,
	// и лишь потом Close(). Иначе свежая запись о нас могла вовсе не уйти в DHT —
	// раунд выглядел бы успешным, а анонса не было (нас никто не находил бы).
	finishAnnounce := func() {
		if port <= 0 {
			a.Close() // мы не анонсируем — ждать нечего
			return
		}
		a.StopTraversing()
		select {
		case <-a.Finished():
		case <-time.After(announceGrace):
		}
		a.Close()
	}

	ctx, cancel := context.WithTimeout(ctx, roundTimeout)
	defer cancel()

	seen := make(map[string]bool)
	var out []string
	for {
		select {
		case <-ctx.Done():
			finishAnnounce() // таймаут поиска — не ошибка, но анонс дошлём
			return out, nil
		case <-a.Finished():
			a.Close()
			return out, nil
		case v, ok := <-a.Peers:
			if !ok {
				finishAnnounce()
				return out, nil
			}
			for _, p := range v.Peers {
				s, ok := usableAddr(p)
				if !ok || seen[s] {
					continue
				}
				seen[s] = true
				out = append(out, s)
				if len(out) >= maxPeersPerRound {
					finishAnnounce()
					return out, nil
				}
			}
		}
	}
}

// usableAddr отсеивает заведомо бесполезные записи: нулевые порты, а также
// приватные/loopback/link-local адреса. Последние в публичной DHT означают либо
// мусор, либо чужую локалку — долбить их без толку, а вот на своей машине они бы
// увели пробитие в собственную подсеть.
func usableAddr(p krpc.NodeAddr) (string, bool) {
	ip := p.IP.To4()
	if ip == nil || p.Port <= 0 || p.Port > 65535 {
		return "", false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return "", false
	}
	return (&net.UDPAddr{IP: ip, Port: p.Port}).String(), true
}

// SaveNodes сбрасывает таблицу узлов DHT в файл кэша (не чаще раза в minInterval).
func (d *Discoverer) SaveNodes(minInterval time.Duration) error {
	d.mu.Lock()
	if d.closed || d.nodesPath == "" || time.Since(d.lastSave) < minInterval {
		d.mu.Unlock()
		return nil
	}
	d.lastSave = time.Now()
	d.mu.Unlock()

	nodes := d.srv.Nodes()
	if len(nodes) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(d.nodesPath), 0o755); err != nil {
		return err
	}
	return dht.WriteNodesToFile(nodes, d.nodesPath)
}

// Close останавливает DHT-узел, напоследок сохранив кэш узлов.
func (d *Discoverer) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.mu.Unlock()

	_ = d.saveNow()
	d.srv.Close()
}

func (d *Discoverer) saveNow() error {
	if d.nodesPath == "" {
		return nil
	}
	nodes := d.srv.Nodes()
	if len(nodes) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(d.nodesPath), 0o755); err != nil {
		return err
	}
	return dht.WriteNodesToFile(nodes, d.nodesPath)
}

// Infohash выводит инфохэш сети на сутки day (по UTC).
//
// HMAC, а не просто хэш: ключ сети — секрет, и инфохэш не должен давать по нему
// ничего. Дата в UTC — чтобы у сторон в разных часовых поясах ключ совпадал.
// Формат даты — YYYY-MM-DD, метка домена отделяет это применение ключа от
// сетевого тега (sha256("lanmesh-tag|"+ключ)) и от шифрования трафика.
func Infohash(key [crypto.KeySize]byte, day time.Time) [20]byte {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte("lanmesh-dht|v1|" + day.UTC().Format("2006-01-02")))
	sum := mac.Sum(nil)
	var ih [20]byte
	copy(ih[:], sum[:20])
	return ih
}
