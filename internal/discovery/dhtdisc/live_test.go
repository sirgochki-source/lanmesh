package dhtdisc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
)

// Живая проверка боевого пути: два независимых узла с ОДНИМ ключом сети должны
// найти анонсы друг друга в настоящей публичной DHT. Ходит в интернет, поэтому по
// умолчанию пропускается — включать явно:
//
//	LANMESH_DHT_LIVE=1 go test ./internal/discovery/dhtdisc -run Live -v
//
// Провал теста означает одно из двух: DHT режет провайдер/файрвол либо сломалось
// само обнаружение. Различить помогает число узлов в логе: 0 — режут.
func TestLiveDHTRendezvous(t *testing.T) {
	if os.Getenv("LANMESH_DHT_LIVE") == "" {
		t.Skip("живой тест DHT: задай LANMESH_DHT_LIVE=1")
	}

	// Имя сети уникально на прогон — чтобы не поймать записи прошлых запусков.
	name := fmt.Sprintf("lanmesh-live-%d", time.Now().UnixNano())
	key := crypto.DeriveNetworkKey(name, "пароль-для-живого-теста")
	t.Logf("сеть %q, инфохэш %x", name, Infohash(key, time.Now().UTC()))

	a, err := New("")
	if err != nil {
		t.Fatalf("узел A: %v", err)
	}
	defer a.Close()
	b, err := New("")
	if err != nil {
		t.Fatalf("узел B: %v", err)
	}
	defer b.Close()

	ctx := context.Background()
	start := time.Now()
	if err := a.Bootstrap(ctx); err != nil {
		t.Logf("A bootstrap: %v", err)
	}
	t.Logf("A: bootstrap за %.1fс, узлов %d", time.Since(start).Seconds(), a.NumNodes())
	start = time.Now()
	if err := b.Bootstrap(ctx); err != nil {
		t.Logf("B bootstrap: %v", err)
	}
	t.Logf("B: bootstrap за %.1fс, узлов %d", time.Since(start).Seconds(), b.NumNodes())
	if a.NumNodes() == 0 && b.NumNodes() == 0 {
		t.Fatal("ни одного узла DHT — почти наверняка её режет провайдер или файрвол")
	}

	const portA, portB = 40001, 40002
	round := func(who string, d *Discoverer, port int) []string {
		start := time.Now()
		found, err := d.Round(ctx, key, port)
		t.Logf("%s: раунд за %.1fс, найдено %v (err=%v)", who, time.Since(start).Seconds(), found, err)
		return found
	}

	round("A", a, portA)          // первому находить ещё некого — только анонс
	fb := round("B", b, portB)    // B должен увидеть анонс A
	fa := round("A(2)", a, portA) // A на втором круге — анонс B

	if !hasPort(fb, portA) && !hasPort(fa, portB) {
		t.Fatalf("узлы не нашли анонсы друг друга: B=%v A=%v", fb, fa)
	}
}

func hasPort(list []string, port int) bool {
	suffix := fmt.Sprintf(":%d", port)
	for _, s := range list {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}
