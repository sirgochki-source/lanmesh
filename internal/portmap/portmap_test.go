package portmap

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

// Отбраковка неанонсируемых адресов. Без неё фича ВРЕДНА: пиры будут долбиться
// в мусорный адрес вместо рабочих кандидатов, то есть станет хуже, чем без
// проброса вовсе.
func TestUsableRejectsUnroutable(t *testing.T) {
	stun := netip.MustParseAddr("203.0.113.5")
	cases := []struct {
		name string
		ext  string
		want bool
	}{
		{"CGNAT: роутер сам за операторским NAT", "100.64.1.1", false},
		{"приватный: двойной NAT", "192.168.1.1", false},
		{"приватный 10/8", "10.0.0.1", false},
		{"link-local", "169.254.1.1", false},
		{"loopback", "127.0.0.1", false},
		{"публичный, но чужой IP — не наш маппинг", "198.51.100.7", false},
		{"публичный и совпал со STUN", "203.0.113.5", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Usable(netip.MustParseAddr(c.ext), stun); got != c.want {
				t.Fatalf("Usable(%s) = %v, ожидали %v", c.ext, got, c.want)
			}
		})
	}
}

// STUN промолчал: проброшенный адрес — единственный источник внешнего адреса,
// поэтому публичный принимаем без сверки.
func TestUsableWithoutStun(t *testing.T) {
	if !Usable(netip.MustParseAddr("203.0.113.5"), netip.Addr{}) {
		t.Fatal("без STUN публичный адрес обязан приниматься")
	}
	if Usable(netip.MustParseAddr("100.64.1.1"), netip.Addr{}) {
		t.Fatal("без STUN CGNAT-адрес всё равно бесполезен")
	}
}

func TestUsableRejectsInvalidAddr(t *testing.T) {
	if Usable(netip.Addr{}, netip.Addr{}) {
		t.Fatal("невалидный (нулевой) адрес не может быть Usable")
	}
}

// --- NAT-PMP ---

func TestParseNATPMPResponse(t *testing.T) {
	// [версия 0][опкод 129][результат 0][время 0][внутр. порт 25000]
	// [внешн. порт 31337][аренда 7200] + внешний адрес отдаётся отдельным
	// запросом опкода 0, поэтому здесь только порт и аренда.
	raw := []byte{0, 129, 0, 0, 0, 0, 0, 0, 0x61, 0xa8, 0x7a, 0x69, 0, 0, 0x1c, 0x20}
	port, lease, err := parseNATPMPResponse(raw)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if port != 31337 {
		t.Fatalf("внешний порт %d, ожидали 31337", port)
	}
	if lease != 7200*time.Second {
		t.Fatalf("аренда %v, ожидали 2ч", lease)
	}
}

func TestParseNATPMPResponseRejectsErrorCode(t *testing.T) {
	raw := []byte{0, 129, 0, 2 /* out of resources */, 0, 0, 0, 0, 0x61, 0xa8, 0, 0, 0, 0, 0, 0}
	if _, _, err := parseNATPMPResponse(raw); err == nil {
		t.Fatal("ненулевой код результата обязан вернуть ошибку")
	}
}

func TestParseNATPMPResponseRejectsShort(t *testing.T) {
	if _, _, err := parseNATPMPResponse([]byte{0, 129, 0, 0}); err == nil {
		t.Fatal("короткий ответ обязан вернуть ошибку")
	}
}

func TestParseNATPMPExternalAddr(t *testing.T) {
	// [версия 0][опкод 128][результат 0][время 0][адрес 203.0.113.5]
	raw := []byte{0, 128, 0, 0, 0, 0, 0, 0, 203, 0, 113, 5}
	addr, err := parseNATPMPExternalAddr(raw)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if addr != netip.MustParseAddr("203.0.113.5") {
		t.Fatalf("получили %s", addr)
	}
}

func TestParseNATPMPExternalAddrRejectsWrongOpcode(t *testing.T) {
	// опкод 129 (ответ на маппинг) вместо ожидаемого 128 (ответ на адрес).
	raw := []byte{0, 129, 0, 0, 0, 0, 0, 0, 203, 0, 113, 5}
	if _, err := parseNATPMPExternalAddr(raw); err == nil {
		t.Fatal("неверный опкод обязан вернуть ошибку")
	}
}

// --- PCP ---

// pcpMapResponse — тестовый помощник: собирает байты ответа PCP MAP.
func pcpMapResponse(nonce [12]byte, port uint16, addr netip.Addr, leaseSec uint32) []byte {
	raw := make([]byte, 60)
	raw[0] = 2
	raw[1] = 0x81
	// raw[3] = 0 — код результата "успех".
	binary.BigEndian.PutUint32(raw[4:8], leaseSec)
	copy(raw[24:36], nonce[:])
	raw[36] = 17
	binary.BigEndian.PutUint16(raw[42:44], port)
	addrBytes := v4InV6(addr)
	copy(raw[44:60], addrBytes[:])
	return raw
}

func TestParsePCPRejectsForeignNonce(t *testing.T) {
	var mine, other [12]byte
	mine[0], other[0] = 1, 2
	raw := pcpMapResponse(other, 31337, netip.MustParseAddr("203.0.113.5"), 7200)
	if _, _, err := parsePCPMap(raw, mine); err == nil {
		t.Fatal("ответ с чужим nonce принят как свой")
	}
}

func TestParsePCPMapSuccess(t *testing.T) {
	var nonce [12]byte
	nonce[0] = 7
	raw := pcpMapResponse(nonce, 31337, netip.MustParseAddr("203.0.113.5"), 7200)
	ext, lease, err := parsePCPMap(raw, nonce)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if ext != netip.MustParseAddrPort("203.0.113.5:31337") {
		t.Fatalf("получили %s", ext)
	}
	if lease != 7200*time.Second {
		t.Fatalf("аренда %v, ожидали 2ч", lease)
	}
}

func TestParsePCPMapRejectsErrorCode(t *testing.T) {
	var nonce [12]byte
	raw := pcpMapResponse(nonce, 31337, netip.MustParseAddr("203.0.113.5"), 7200)
	raw[3] = 3 // NETWORK_FAILURE
	if _, _, err := parsePCPMap(raw, nonce); err == nil {
		t.Fatal("ненулевой код результата обязан вернуть ошибку")
	}
}

func TestParsePCPMapRejectsWrongVersion(t *testing.T) {
	var nonce [12]byte
	raw := pcpMapResponse(nonce, 31337, netip.MustParseAddr("203.0.113.5"), 7200)
	raw[0] = 0 // NAT-PMP-only роутер отвечает версией 0 на непонятный ему PCP-запрос
	if _, _, err := parsePCPMap(raw, nonce); err == nil {
		t.Fatal("чужая версия протокола обязана вернуть ошибку")
	}
}

func TestParsePCPMapRejectsShort(t *testing.T) {
	if _, _, err := parsePCPMap(make([]byte, 40), [12]byte{}); err == nil {
		t.Fatal("короткий ответ обязан вернуть ошибку")
	}
}

// --- UPnP ---

func TestParseExternalIP(t *testing.T) {
	const resp = `<?xml version="1.0"?><s:Envelope><s:Body>` +
		`<u:GetExternalIPAddressResponse><NewExternalIPAddress>203.0.113.5` +
		`</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`
	got, err := parseExternalIP(resp)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if got != netip.MustParseAddr("203.0.113.5") {
		t.Fatalf("получили %s", got)
	}
}

func TestParseExternalIPRejectsEmpty(t *testing.T) {
	const resp = `<s:Envelope><s:Body><u:GetExternalIPAddressResponse>` +
		`</u:GetExternalIPAddressResponse></s:Body></s:Envelope>`
	if _, err := parseExternalIP(resp); err == nil {
		t.Fatal("пустой NewExternalIPAddress обязан вернуть ошибку")
	}
}

// descXML — реалистичное описание IGD с вложением root->WANDevice->
// WANConnectionDevice->serviceList, как у большинства бытовых роутеров.
// Пространство имён по умолчанию на <root> и переносы строк внутри
// <controlURL> — намеренно, ровно то, из-за чего регулярка на таких ответах
// ломается непредсказуемо, а encoding/xml с узкой структурой — нет.
const descXML = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
    <deviceList>
      <device>
        <deviceType>urn:schemas-upnp-org:device:WANDevice:1</deviceType>
        <deviceList>
          <device>
            <deviceType>urn:schemas-upnp-org:device:WANConnectionDevice:1</deviceType>
            <serviceList>
              <service>
                <serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType>
                <controlURL>
                  /upnp/control/WANIPConn1
                </controlURL>
              </service>
            </serviceList>
          </device>
        </deviceList>
      </device>
    </deviceList>
  </device>
</root>`

func TestParseControlURL(t *testing.T) {
	got, err := parseControlURL(descXML)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if got != "/upnp/control/WANIPConn1" {
		t.Fatalf("controlURL = %q", got)
	}
}

func TestFindWANServicePPP(t *testing.T) {
	const desc = `<root xmlns="urn:schemas-upnp-org:device-1-0"><device>
      <serviceList>
        <service>
          <serviceType>urn:schemas-upnp-org:service:WANPPPConnection:1</serviceType>
          <controlURL>/ctl/IPConn</controlURL>
        </service>
      </serviceList>
    </device></root>`
	cu, st, err := findWANService(desc)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if cu != "/ctl/IPConn" || st != "urn:schemas-upnp-org:service:WANPPPConnection:1" {
		t.Fatalf("controlURL=%q serviceType=%q", cu, st)
	}
}

func TestParseControlURLNotFound(t *testing.T) {
	const desc = `<root xmlns="urn:schemas-upnp-org:device-1-0"><device>
      <serviceList><service>
        <serviceType>urn:schemas-upnp-org:service:Layer3Forwarding:1</serviceType>
        <controlURL>/ctl/L3F</controlURL>
      </service></serviceList>
    </device></root>`
	if _, err := parseControlURL(desc); err == nil {
		t.Fatal("отсутствие WANIPConnection/WANPPPConnection обязано вернуть ошибку")
	}
}

func TestParseSSDPLocation(t *testing.T) {
	resp := "HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=1800\r\n" +
		"LOCATION: http://192.168.1.1:5000/rootDesc.xml\r\n" +
		"ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n" +
		"\r\n"
	loc, err := parseSSDPLocation([]byte(resp))
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if loc != "http://192.168.1.1:5000/rootDesc.xml" {
		t.Fatalf("LOCATION = %q", loc)
	}
}

func TestParseSSDPLocationMissing(t *testing.T) {
	resp := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\n\r\n"
	if _, err := parseSSDPLocation([]byte(resp)); err == nil {
		t.Fatal("отсутствие LOCATION обязано вернуть ошибку")
	}
}

func TestResolveURL(t *testing.T) {
	got, err := resolveURL("http://192.168.1.1:5000/rootDesc.xml", "/upnp/control/WANIPConn1")
	if err != nil {
		t.Fatalf("resolveURL: %v", err)
	}
	if got != "http://192.168.1.1:5000/upnp/control/WANIPConn1" {
		t.Fatalf("got %q", got)
	}
}
