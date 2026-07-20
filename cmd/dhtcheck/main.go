// Command dhtcheck — диагностика обнаружения через DHT для конкретной сети.
//
// Поднимает отдельный узел DHT (не трогая работающую панель), считает инфохэш
// сети и смотрит, кто на нём анонсирован. Отвечает на вопрос «нас вообще видно
// со стороны и кого видим мы» — в режиме без серверов диагностику иначе взять
// негде: логи такой сети сознательно никуда не уходят.
//
//	dhtcheck -network DHT_Test -password секрет -dht
//	dhtcheck -network DHT_Test -from-config      // имя+пароль из config.json панели
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/discovery/dhtdisc"
)

func main() {
	network := flag.String("network", "", "имя сети")
	password := flag.String("password", "", "пароль сети")
	fromConfig := flag.Bool("from-config", false, "взять имя/пароль/режим из config.json панели (пароль в вывод не попадает)")
	relay := flag.Bool("dht-relay", false, "режим сети — dht+relay (иначе чистый dht)")
	flag.Parse()

	mode := "dht"
	if *relay {
		mode = "dht+relay"
	}

	if *fromConfig {
		name, pass, m, err := fromPanelConfig(*network)
		if err != nil {
			fmt.Println("конфиг:", err)
			os.Exit(1)
		}
		*network, *password, mode = name, pass, m
		fmt.Printf("из конфига панели: сеть %q, режим %s\n", name, mode)
	}
	if *network == "" || *password == "" {
		flag.Usage()
		os.Exit(2)
	}

	key := crypto.DeriveNetworkKeyMode(*network, *password, mode)
	now := time.Now().UTC()
	fmt.Printf("инфохэш на сегодня (%s): %x\n", now.Format("2006-01-02"), dhtdisc.Infohash(key, now))
	fmt.Printf("инфохэш на вчера:              %x\n\n", dhtdisc.Infohash(key, now.AddDate(0, 0, -1)))

	d, err := dhtdisc.New("") // без кэша: смотрим честно, как чужой клиент
	if err != nil {
		fmt.Println("DHT:", err)
		os.Exit(1)
	}
	defer d.Close()

	ctx := context.Background()
	t0 := time.Now()
	if err := d.Bootstrap(ctx); err != nil {
		fmt.Println("bootstrap:", err)
	}
	fmt.Printf("вход в DHT: %.1fс, узлов %d\n", time.Since(t0).Seconds(), d.NumNodes())
	if d.NumNodes() == 0 {
		fmt.Println("\nDHT недоступна — её режет провайдер или файрвол. В этом режиме сеть не заработает.")
		return
	}

	// port=0 — только ищем, себя не анонсируем: не подмешиваем в выдачу свой же
	// адрес и не мусорим в сети ради диагностики.
	t0 = time.Now()
	found, err := d.Round(ctx, key, 0)
	fmt.Printf("поиск: %.1fс", time.Since(t0).Seconds())
	if err != nil {
		fmt.Printf(", ошибка: %v", err)
	}
	fmt.Printf("\n\nанонсировано на этом инфохэше: %d\n", len(found))
	for _, a := range found {
		fmt.Println("  ", a)
	}
	if len(found) == 0 {
		fmt.Println("\nНи одного анонса. Значит на этот ключ сейчас никто не заявлен — ни ты, ни друг.")
		fmt.Println("Проверь, что панель поднята и сеть в ней в режиме DHT.")
	}
}

// fromPanelConfig достаёт профиль сети из конфига панели. Пароль возвращается для
// вычисления ключа, но наружу не печатается.
func fromPanelConfig(name string) (string, string, string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", "", "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "lanmesh", "config.json"))
	if err != nil {
		return "", "", "", err
	}
	var cfg struct {
		Networks []struct {
			Name      string `json:"name"`
			Password  string `json:"password"`
			Discovery string `json:"discovery"`
		} `json:"networks"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", "", "", err
	}
	for _, p := range cfg.Networks {
		if name == "" || p.Name == name {
			m := p.Discovery
			if m == "" {
				m = "signal"
			}
			return p.Name, p.Password, m, nil
		}
	}
	return "", "", "", fmt.Errorf("сеть %q в конфиге не найдена", name)
}
