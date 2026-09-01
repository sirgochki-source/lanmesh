package portmap

import (
	"strings"
	"testing"
)

const routeHeader = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"

// Обычный случай: один маршрут по умолчанию плюс связанный маршрут. Заодно
// фиксирует ПОРЯДОК БАЙТ — 0190A8C0 обязан читаться как 192.168.144.1
// (проверено на живой системе против `ip route`); перепутанный порядок дал бы
// 1.144.168.192, и PCP/NAT-PMP стучались бы не туда, молча не находя роутер.
func TestParseDefaultGatewaySingle(t *testing.T) {
	in := routeHeader +
		"eth0\t00000000\t0190A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"eth0\t0090A8C0\t00000000\t0001\t0\t0\t0\t00F0FFFF\t0\t0\t0\n"
	gw, err := parseDefaultGateway(strings.NewReader(in))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if gw.String() != "192.168.144.1" {
		t.Fatalf("шлюз разобран как %s, ожидали 192.168.144.1", gw)
	}
}

// Несколько маршрутов по умолчанию (две сетевые карты или поднятый VPN): берём
// с наименьшей метрикой, как выбирает и ядро. Порядок строк влиять не должен.
func TestParseDefaultGatewayLowestMetric(t *testing.T) {
	in := routeHeader +
		"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"eth0\t00000000\t0190A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n"
	gw, err := parseDefaultGateway(strings.NewReader(in))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if gw.String() != "192.168.144.1" {
		t.Fatalf("выбран %s, ожидали шлюз с меньшей метрикой 192.168.144.1", gw)
	}
}

// Края, каждый из которых обязан быть отброшен: маршрут без RTF_UP, маршрут без
// RTF_GATEWAY, нулевой шлюз, мусор, пустая таблица. Ни один не должен стать
// ответом — иначе каскад пошёл бы долбиться в несуществующий адрес.
func TestParseDefaultGatewaySkipsUnusable(t *testing.T) {
	cases := map[string]string{
		"без RTF_UP":      "eth0\t00000000\t0190A8C0\t0002\t0\t0\t0\t00000000\t0\t0\t0\n",
		"без RTF_GATEWAY": "eth0\t00000000\t0190A8C0\t0001\t0\t0\t0\t00000000\t0\t0\t0\n",
		"нулевой шлюз":    "eth0\t00000000\t00000000\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
		"мусор":           "это не таблица маршрутов вовсе\n",
		"пусто":           "",
	}
	for name, body := range cases {
		if gw, err := parseDefaultGateway(strings.NewReader(routeHeader + body)); err == nil {
			t.Fatalf("%s: ожидали ошибку, получили шлюз %s", name, gw)
		}
	}
}
