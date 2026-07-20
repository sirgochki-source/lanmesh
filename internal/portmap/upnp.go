package portmap

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	ssdpAddr = "239.255.255.250:1900"
	ssdpST   = "urn:schemas-upnp-org:device:InternetGatewayDevice:1"

	// maxUPnPBodySize — потолок на тело HTTP-ответа роутера (описание устройства
	// или SOAP-ответ). Роутер — не доверенный сервер: без потолка испорченный или
	// откровенно враждебный ответ мог бы вычитываться неограниченно.
	maxUPnPBodySize = 1 << 20 // 1 МиБ, с большим запасом (обычное описание — единицы КиБ)
)

// wanServiceTypes — сервисы, размечающие проброс порта. WANIPConnection:1 —
// обычный случай; WANPPPConnection:1 — роутер в режиме PPPoE-моста, встречается
// реже, но тоже в поле.
var wanServiceTypes = []string{
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

// upnpMapper — проброс через UPnP-IGD: SSDP-обнаружение -> GET описания
// устройства -> SOAP-вызовы. Самый тяжёлый из трёх протоколов, поэтому
// controlURL/serviceType кэшируются в мэппере после первого успеха — при
// продлении аренды повторять SSDP и разбор XML незачем.
type upnpMapper struct {
	controlURL  string
	serviceType string
}

func (m *upnpMapper) name() string { return "upnp" }

func (m *upnpMapper) add(ctx context.Context, localPort int) (netip.AddrPort, time.Duration, error) {
	if m.controlURL == "" {
		loc, err := discoverSSDP(ctx)
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("upnp: SSDP: %w", err)
		}
		descXML, err := fetchDescription(ctx, loc)
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("upnp: описание устройства: %w", err)
		}
		cu, st, err := findWANService(descXML)
		if err != nil {
			return netip.AddrPort{}, 0, err
		}
		abs, err := resolveURL(loc, cu)
		if err != nil {
			return netip.AddrPort{}, 0, fmt.Errorf("upnp: controlURL: %w", err)
		}
		m.controlURL = abs
		m.serviceType = st
	}

	extIP, err := upnpGetExternalIP(ctx, m.controlURL, m.serviceType)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}

	routerIP, err := hostAddr(m.controlURL)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("upnp: адрес роутера из controlURL: %w", err)
	}
	// Роутеру нужен НАШ LAN-адрес (NewInternalClient), не адрес назначения —
	// иначе AddPortMapping перешлёт порт не туда.
	localIP, err := localAddrToward(routerIP)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("upnp: локальный адрес: %w", err)
	}

	if err := upnpAddPortMapping(ctx, m.controlURL, m.serviceType, localIP, localPort, mappingLease); err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("upnp: AddPortMapping: %w", err)
	}

	// В отличие от PCP/NAT-PMP, AddPortMapping не возвращает согласованный порт —
	// либо роутер даёт РОВНО тот внешний порт, что мы запросили, либо отказывает.
	return netip.AddrPortFrom(extIP, uint16(localPort)), mappingLease, nil
}

func (m *upnpMapper) remove(ctx context.Context, localPort int) {
	if m.controlURL == "" {
		return // маппинг ни разу не создавался — снимать нечего
	}
	_ = upnpDeletePortMapping(ctx, m.controlURL, m.serviceType, localPort)
}

// discoverSSDP шлёт M-SEARCH мультикастом и возвращает LOCATION первого
// ответившего IGD. Шлюз обходимся без него — мультикаст сам находит роутера.
func discoverSSDP(ctx context.Context) (string, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return "", err
	}
	defer conn.Close()
	defer closeOnDone(ctx, conn)()

	dst, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return "", err
	}

	req := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + ssdpAddr + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + ssdpST + "\r\n" +
		"\r\n"
	if _, err := conn.WriteToUDP([]byte(req), dst); err != nil {
		return "", err
	}

	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return "", fmt.Errorf("upnp: SSDP не ответил: %w", err)
		}
		if loc, err := parseSSDPLocation(buf[:n]); err == nil {
			return loc, nil
		}
		// Не тот ответ (реклама постороннего устройства/битый пакет за общей
		// мультикаст-группой) — читаем следующий до отмены/дедлайна ctx.
	}
}

// parseSSDPLocation достаёт заголовок LOCATION из SSDP-ответа. Чистая функция,
// тестируется без сети.
func parseSSDPLocation(resp []byte) (string, error) {
	const prefix = "location:"
	for _, line := range strings.Split(string(resp), "\r\n") {
		if len(line) > len(prefix) && strings.EqualFold(line[:len(prefix)], prefix) {
			return strings.TrimSpace(line[len(prefix):]), nil
		}
	}
	return "", errors.New("upnp: в ответе SSDP нет LOCATION")
}

// fetchDescription скачивает XML-описание устройства по LOCATION.
func fetchDescription(ctx context.Context, loc string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upnp: GET %s: код %d", loc, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUPnPBodySize))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// upnpService/upnpDevice/upnpRoot — узкая структура под encoding/xml вместо
// регулярки: ответы роутеров отличаются пространствами имён (свои префиксы
// s:/u:, xmlns по умолчанию на <root>) и переносами внутри значений, а
// encoding/xml без объявленного namespace в теге сопоставляет ПО ЛОКАЛЬНОМУ
// ИМЕНИ, то есть не зависит от префикса/xmlns — проверено на реальной форме
// ответа (namespace по умолчанию на root + непривязанные префиксы s:/u: в SOAP).
// upnpDevice рекурсивный (сам ссылается на себя через DeviceList), потому что
// дерево IGD обычно вложено на два уровня: root device -> WANDevice ->
// WANConnectionDevice -> serviceList.
type upnpService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

type upnpDevice struct {
	ServiceList struct {
		Services []upnpService `xml:"service"`
	} `xml:"serviceList"`
	DeviceList struct {
		Devices []upnpDevice `xml:"device"`
	} `xml:"deviceList"`
}

type upnpRoot struct {
	XMLName xml.Name   `xml:"root"`
	Device  upnpDevice `xml:"device"`
}

// findWANService обходит дерево устройств в поисках WANIPConnection:1 /
// WANPPPConnection:1 и возвращает его controlURL и точный serviceType (нужен
// дальше для SOAPAction и xmlns:u — это разные сервисы с разными именами
// действий, использовать не тот namespace значило бы получить SOAP Fault).
func findWANService(descXML string) (controlURL, serviceType string, err error) {
	var root upnpRoot
	if err := xml.Unmarshal([]byte(descXML), &root); err != nil {
		return "", "", fmt.Errorf("upnp: разбор описания устройства: %w", err)
	}
	cu, st, ok := findServiceIn(root.Device)
	if !ok {
		return "", "", errors.New("upnp: сервис WANIPConnection/WANPPPConnection не найден в описании")
	}
	return cu, st, nil
}

func findServiceIn(d upnpDevice) (controlURL, serviceType string, ok bool) {
	for _, s := range d.ServiceList.Services {
		st := strings.TrimSpace(s.ServiceType)
		for _, want := range wanServiceTypes {
			if st == want {
				return strings.TrimSpace(s.ControlURL), st, true
			}
		}
	}
	for _, sub := range d.DeviceList.Devices {
		if cu, st, ok := findServiceIn(sub); ok {
			return cu, st, true
		}
	}
	return "", "", false
}

// parseControlURL — только controlURL, без serviceType: имя и сигнатура
// зафиксированы заданием для случаев, когда serviceType не нужен.
func parseControlURL(descXML string) (string, error) {
	cu, _, err := findWANService(descXML)
	return cu, err
}

// resolveURL превращает controlURL (в описаниях обычно относительный путь,
// напр. "/upnp/control/WANIPConn1") в абсолютный URL относительно LOCATION.
func resolveURL(base, ref string) (string, error) {
	bu, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return bu.ResolveReference(ru).String(), nil
}

// hostAddr достаёт IP роутера из controlURL (тот же хост, что LOCATION).
func hostAddr(rawURL string) (netip.Addr, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return netip.Addr{}, err
	}
	return netip.ParseAddr(u.Hostname())
}

// soapEnvelope собирает тело SOAP-запроса под конкретный serviceType/action.
func soapEnvelope(serviceType, action, args string) string {
	return `<?xml version="1.0"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">` +
		`<s:Body><u:` + action + ` xmlns:u="` + serviceType + `">` + args + `</u:` + action + `></s:Body></s:Envelope>`
}

// soapCall шлёт SOAP-запрос на controlURL и возвращает тело ответа.
func soapCall(ctx context.Context, controlURL, serviceType, action, body string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, serviceType, action))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxUPnPBodySize))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upnp: %s: код %d, тело %.200s", action, resp.StatusCode, respBody)
	}
	return string(respBody), nil
}

func upnpGetExternalIP(ctx context.Context, controlURL, serviceType string) (netip.Addr, error) {
	body := soapEnvelope(serviceType, "GetExternalIPAddress", "")
	respXML, err := soapCall(ctx, controlURL, serviceType, "GetExternalIPAddress", body)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("upnp: GetExternalIPAddress: %w", err)
	}
	return parseExternalIP(respXML)
}

// parseExternalIP достаёт NewExternalIPAddress из SOAP-ответа. Чистая функция,
// тестируется на записанном XML без сети.
func parseExternalIP(soapXML string) (netip.Addr, error) {
	var resp struct {
		XMLName xml.Name `xml:"Envelope"`
		Body    struct {
			Resp struct {
				ExternalIP string `xml:"NewExternalIPAddress"`
			} `xml:"GetExternalIPAddressResponse"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal([]byte(soapXML), &resp); err != nil {
		return netip.Addr{}, fmt.Errorf("upnp: разбор ответа GetExternalIPAddress: %w", err)
	}
	ip := strings.TrimSpace(resp.Body.Resp.ExternalIP)
	if ip == "" {
		return netip.Addr{}, errors.New("upnp: в ответе нет NewExternalIPAddress")
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("upnp: не разобрать внешний адрес %q: %w", ip, err)
	}
	return addr, nil
}

func upnpAddPortMapping(ctx context.Context, controlURL, serviceType string, internalClient netip.Addr, port int, lease time.Duration) error {
	args := fmt.Sprintf(
		"<NewRemoteHost></NewRemoteHost>"+
			"<NewExternalPort>%d</NewExternalPort>"+
			"<NewProtocol>UDP</NewProtocol>"+
			"<NewInternalPort>%d</NewInternalPort>"+
			"<NewInternalClient>%s</NewInternalClient>"+
			"<NewEnabled>1</NewEnabled>"+
			"<NewPortMappingDescription>lanmesh</NewPortMappingDescription>"+
			"<NewLeaseDuration>%d</NewLeaseDuration>",
		port, port, internalClient, int(lease/time.Second))
	body := soapEnvelope(serviceType, "AddPortMapping", args)
	_, err := soapCall(ctx, controlURL, serviceType, "AddPortMapping", body)
	return err
}

func upnpDeletePortMapping(ctx context.Context, controlURL, serviceType string, port int) error {
	args := fmt.Sprintf(
		"<NewRemoteHost></NewRemoteHost><NewExternalPort>%d</NewExternalPort><NewProtocol>UDP</NewProtocol>",
		port)
	body := soapEnvelope(serviceType, "DeletePortMapping", args)
	_, err := soapCall(ctx, controlURL, serviceType, "DeletePortMapping", body)
	return err
}
