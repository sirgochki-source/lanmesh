// Package netcache помнит между запусками адреса, по которым пиры реально
// отвечали. При следующем старте они уходят в пробитие СРАЗУ, не дожидаясь
// ответа сигналки: если адрес друга не менялся, линк поднимается на первой
// секунде, а не после первого раунда регистрации.
//
// Файл НЕ шифруется сознательно: config.json рядом хранит пароли сетей открытым
// текстом, поэтому шифрование соседнего файла с адресами ничего не защищает —
// кто прочитал одно, прочитал и другое.
package netcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// ttl — длинный сознательно: проба стоит несколько UDP-пакетов с backoff, а
	// мёртвый адрес отсеется сам. Короткий TTL сделал бы кэш бесполезным.
	ttl = 30 * 24 * time.Hour
	// maxPerPeer — сколько адресов помним на пира: домашний, мобильный, рабочий.
	maxPerPeer = 3
)

type entry struct {
	Addr string    `json:"addr"`
	Seen time.Time `json:"seen"`
}

// Cache — «(тег сети, PeerID) → последние подтверждённые адреса».
type Cache struct {
	path string

	mu      sync.Mutex
	entries map[string][]entry
	dirty   bool

	// saveMu сериализует ФАЙЛОВУЮ часть Save (MkdirAll+WriteFile+Rename) отдельно
	// от mu: mu защищает только карту entries, а два Save подряд (cacheSaveLoop по
	// таймеру и финальный Save из tearDownNode) могут пойти писать один и тот же
	// path+".tmp" почти одновременно — close(nodeStop) не ждёт, пока предыдущий
	// Save дописал файл. Без этого мьютекса гонка могла бы оставить на диске
	// испорченный (частично переписанный) endpoints.json.
	saveMu sync.Mutex
}

// Open читает кэш. Ошибки чтения не возвращаются: битый или отсутствующий файл —
// это просто пустой кэш, ронять из-за него узел незачем.
func Open(path string) *Cache {
	c := &Cache{path: path, entries: map[string][]entry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var stored map[string][]entry
	if json.Unmarshal(data, &stored) != nil {
		return c
	}
	c.entries = stored
	return c
}

func key(tag, id string) string { return tag + "|" + id }

// Get отдаёт живые адреса пира, свежие первыми.
func (c *Cache) Get(tag, id string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	var out []string
	for _, e := range c.entries[key(tag, id)] {
		if now.Sub(e.Seen) < ttl {
			out = append(out, e.Addr)
		}
	}
	return out
}

// Put запоминает адрес, по которому пир ОТВЕТИЛ. Кандидатов сюда класть нельзя:
// кэш накопил бы мусор из DHT и воспроизводил его при каждом старте.
func (c *Cache) Put(tag, id, addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(tag, id)
	list := c.entries[k]
	for i, e := range list {
		if e.Addr == addr {
			// Переставляем в начало, а не просто обновляем Seen на месте: Get
			// обещает "свежие первыми", и без переноса повторно подтверждённый
			// (но не новый) адрес мог бы застрять в хвосте списка.
			e.Seen = time.Now()
			list = append(list[:i:i], list[i+1:]...)
			list = append([]entry{e}, list...)
			c.entries[k], c.dirty = list, true
			return
		}
	}
	list = append([]entry{{Addr: addr, Seen: time.Now()}}, list...)
	if len(list) > maxPerPeer {
		list = list[:maxPerPeer]
	}
	c.entries[k], c.dirty = list, true
}

// Peers перечисляет PeerID, для которых под этим тегом вообще есть записи (живые
// или протухшие — фильтрация по TTL уже в Get). Нужен на подключении сети: до
// первого ответа сигналки (или раунда DHT) PeerID неоткуда взять, кроме как из
// собственного кэша прошлых сессий — а Get(tag, id) требует id заранее.
func (c *Cache) Peers(tag string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := tag + "|"
	out := make([]string, 0, len(c.entries))
	for k := range c.entries {
		if id, ok := strings.CutPrefix(k, prefix); ok {
			out = append(out, id)
		}
	}
	return out
}

// Save пишет кэш атомарно (temp + rename). Зовётся по таймеру и при выходе, а не
// на каждый пакет: файл не должен стать источником дисковой нагрузки под
// игровым трафиком.
//
// path=="" (UserConfigDir() недоступен, см. netcachePath) — Save намеренно
// no-op: иначе WriteFile писал бы ".tmp" в текущий каталог, а Rename(".tmp", "")
// падал бы каждую минуту.
func (c *Cache) Save() error {
	if c.path == "" {
		return nil
	}

	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	now := time.Now()
	live := map[string][]entry{}
	for k, list := range c.entries {
		var keep []entry
		for _, e := range list {
			if now.Sub(e.Seen) < ttl {
				keep = append(keep, e)
			}
		}
		if len(keep) > 0 {
			live[k] = keep
		}
	}
	c.mu.Unlock()

	// Marshal — уже без c.mu: колбэк OnDirectConfirmed (см. session.go) на каждый
	// подтверждённый пакет зовёт c.Put, а сам колбэк выполняется под e.mu
	// движка — мьютексом всего цикла чтения пакетов. Держать c.mu на время
	// Marshal значило бы держать и e.mu, то есть тормозить приём трафика ради
	// записи кэша на диск.
	data, err := json.Marshal(live)
	if err != nil {
		return err
	}

	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}

	// dirty сбрасываем ТОЛЬКО после успешного Rename: если запись провалилась
	// (диск полон, антивирус держит файл), изменения обязаны остаться "грязными",
	// иначе следующий тик тихо ничего не сохранит.
	c.mu.Lock()
	c.dirty = false
	c.mu.Unlock()
	return nil
}
