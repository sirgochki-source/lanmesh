package netcache

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func tmp(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "endpoints.json")
}

// Записали — прочитали после перезагрузки с диска.
func TestPutGetRoundTrip(t *testing.T) {
	path := tmp(t)
	c := Open(path)
	c.Put("тег1", "пир1", "203.0.113.5:31337")
	if err := c.Save(); err != nil {
		t.Fatalf("сохранение: %v", err)
	}

	got := Open(path).Get("тег1", "пир1")
	if len(got) != 1 || got[0] != "203.0.113.5:31337" {
		t.Fatalf("ожидали один адрес, получили %v", got)
	}
}

// Адрес из одной сети не должен всплыть в пробах другой: пробить чужой адрес
// безвредно, но это лишний трафик и лишний шум в диагностике.
func TestTagIsolation(t *testing.T) {
	c := Open(tmp(t))
	c.Put("тег1", "пир1", "203.0.113.5:1")
	if got := c.Get("тег2", "пир1"); len(got) != 0 {
		t.Fatalf("адрес протёк в чужую сеть: %v", got)
	}
}

// Держим три последних адреса: самый свежий вытесняет самый старый.
func TestKeepsLastThree(t *testing.T) {
	c := Open(tmp(t))
	for _, a := range []string{"1.1.1.1:1", "2.2.2.2:2", "3.3.3.3:3", "4.4.4.4:4"} {
		c.Put("тег", "пир", a)
	}
	got := c.Get("тег", "пир")
	if len(got) != maxPerPeer {
		t.Fatalf("ожидали %d адресов, получили %d: %v", maxPerPeer, len(got), got)
	}
	for _, a := range got {
		if a == "1.1.1.1:1" {
			t.Fatal("самый старый адрес не вытеснен")
		}
	}
}

// Протухшие записи не отдаются и не переживают сохранение.
func TestTTLExpiry(t *testing.T) {
	path := tmp(t)
	c := Open(path)
	c.Put("тег", "пир", "203.0.113.5:1")
	c.entries["тег|пир"][0].Seen = time.Now().Add(-ttl - time.Hour)
	if got := c.Get("тег", "пир"); len(got) != 0 {
		t.Fatalf("протухший адрес отдан: %v", got)
	}
}

// Битый файл не должен ронять узел: читается как пустой кэш.
func TestCorruptFileIsEmpty(t *testing.T) {
	path := tmp(t)
	if err := os.WriteFile(path, []byte("{это не json"), 0600); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if got := Open(path).Get("тег", "пир"); len(got) != 0 {
		t.Fatalf("из битого файла что-то прочиталось: %v", got)
	}
}

// Сохранение атомарно: временный файл не остаётся рядом.
func TestSaveLeavesNoTemp(t *testing.T) {
	path := tmp(t)
	c := Open(path)
	c.Put("тег", "пир", "203.0.113.5:1")
	if err := c.Save(); err != nil {
		t.Fatalf("сохранение: %v", err)
	}
	files, _ := os.ReadDir(filepath.Dir(path))
	if len(files) != 1 {
		t.Fatalf("рядом остался мусор: %v", files)
	}
}

// Put при повторном подтверждении уже известного адреса обязан переставить его
// в начало списка, а не просто обновить Seen на месте — иначе doc-комментарий
// Get ("свежие первыми") не выполнялся бы для этого случая.
func TestPutMovesReconfirmedEntryToFront(t *testing.T) {
	c := Open(tmp(t))
	c.Put("тег", "пир", "1.1.1.1:1")
	c.Put("тег", "пир", "2.2.2.2:2")
	// Переподтверждаем самый старый адрес — он обязан оказаться первым.
	c.Put("тег", "пир", "1.1.1.1:1")

	got := c.Get("тег", "пир")
	if len(got) != 2 || got[0] != "1.1.1.1:1" || got[1] != "2.2.2.2:2" {
		t.Fatalf("после переподтверждения ожидали [1.1.1.1:1 2.2.2.2:2] первым свежим, получили %v", got)
	}
}

// Save обязан сохранять dirty=true, если запись на диск провалилась (например,
// каталог занят файлом с тем же именем и MkdirAll падает) — иначе следующий
// тик cacheSaveLoop тихо ничего не сохранит.
func TestSaveKeepsDirtyOnFailure(t *testing.T) {
	dir := t.TempDir()
	// Кладём ФАЙЛ на месте каталога, где Save ожидает MkdirAll — гарантированная
	// и детерминированная ошибка записи, не зависящая от прав ОС.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	path := filepath.Join(blocker, "endpoints.json") // родитель — файл, не каталог

	c := Open(path)
	c.Put("тег", "пир", "203.0.113.5:1")
	if err := c.Save(); err == nil {
		t.Fatal("ожидали ошибку записи (родитель — не каталог), получили nil")
	}
	if !c.dirty {
		t.Fatal("Save сбросил dirty при неудачной записи на диск")
	}
}

// Save с пустым path (см. netcachePath: UserConfigDir() недоступен) обязан
// быть no-op, а не пытаться писать ".tmp" в текущем каталоге.
func TestSaveNoopOnEmptyPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	stray := filepath.Join(cwd, ".tmp")
	os.Remove(stray) // подчищаем на случай мусора от прошлого упавшего прогона

	c := Open("")
	c.Put("тег", "пир", "203.0.113.5:1")
	if err := c.Save(); err != nil {
		t.Fatalf("Save с пустым path обязан быть no-op, получили ошибку: %v", err)
	}
	if _, err := os.Stat(stray); err == nil {
		os.Remove(stray)
		t.Fatal("Save с пустым path создал .tmp в текущем каталоге")
	}
}

// Peers — единственный способ узнать, кого пробовать пробитием ДО первого
// ответа сигналки/раунда DHT (см. app.AddNetworkMode): PeerID оттуда мы ещё не
// получали, а Get(tag, id) требует id заранее. Список должен быть по тегу и не
// цеплять чужие сети.
func TestPeersListsIDsUnderTag(t *testing.T) {
	c := Open(tmp(t))
	c.Put("тег1", "пирA", "203.0.113.5:1")
	c.Put("тег1", "пирB", "203.0.113.6:2")
	c.Put("тег2", "пирC", "203.0.113.7:3")

	got := c.Peers("тег1")
	sort.Strings(got)
	if len(got) != 2 || got[0] != "пирA" || got[1] != "пирB" {
		t.Fatalf("Peers(тег1) = %v, ожидали [пирA пирB]", got)
	}
	if len(c.Peers("тег3")) != 0 {
		t.Fatal("Peers вернул пиров для тега, которого не было")
	}
}
