package panel

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/sirgochki-source/lanmesh/internal/app"
	"github.com/sirgochki-source/lanmesh/internal/defaults"
)

// Серверы по умолчанию — общие с headless-клиентом, см. internal/defaults
// (плейсхолдеры; боевые адреса подставляются в настройках панели или config.json).
var (
	defaultSignalURLs = defaults.SignalURLs
	defaultRelayAddr  = defaults.RelayAddr
)


// netTag — hex-тег сети из профиля (имя+пароль+режим обнаружения — он вшит в
// ключ, см. crypto.DeriveNetworkKeyMode). Нужен, чтобы сопоставлять сети панели
// (она шлёт тег) с сохранёнными профилями.
func netTag(p NetProfile) string {
	return app.NetworkTag(p.Name, p.Password, p.Discovery)
}

// NetProfile — одна сохранённая сеть (мультисеть «как Radmin»). Пароль храним,
// иначе автоподключение и повторный вход невозможны; config.json пишется 0600.
type NetProfile struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	// Discovery — способ обнаружения пиров этой сети: пусто/"signal" (сигналки)
	// или "dht" (публичная DHT, ни одного обращения к серверам). Задаётся при
	// добавлении сети и не меняется на ходу: смена режима — это, по сути, другой
	// способ вообще существовать в сети, проще выйти и войти заново.
	Discovery string `json:"discovery,omitempty"`
}

// Config — сохранённые настройки. Networks — список сетей, к которым узел
// подключается при старте. Legacy-поля Network/Password/Remember читаются со
// старых конфигов и мигрируются в Networks (см. loadConfig).
type Config struct {
	Networks []NetProfile `json:"networks,omitempty"`

	// Legacy (одна сеть) — только для миграции.
	Network  string `json:"network,omitempty"`
	Password string `json:"password,omitempty"`
	Remember bool   `json:"remember,omitempty"`

	// SendLogs — отправлять ли диагностику на сигналку. Указатель, чтобы отличить
	// «выключено» от «в старом конфиге поля не было»: по умолчанию включено.
	SendLogs *bool `json:"sendLogs,omitempty"`
	// PortMap — пробрасывать ли порт на роутере (PCP/NAT-PMP/UPnP) и открывать
	// входящее правило брандмауэра. Указатель по тому же образцу, что и
	// SendLogs: отсутствие поля в старом конфиге читается как «включено», чтобы
	// обновление не выключило фичу молча существующим пользователям.
	PortMap *bool `json:"portMap,omitempty"`
	// Signals — переопределённый список сигналок; пусто = defaultSignalURLs.
	Signals []string `json:"signals,omitempty"`
	// Relay — переопределённый ретранслятор; nil = defaultRelayAddr, "" = без relay.
	Relay *string `json:"relay,omitempty"`
	// SelfName — своё отображаемое имя узла; пусто = os.Hostname() (как раньше).
	SelfName string `json:"selfName,omitempty"`
	// Port — постоянный локальный UDP-порт узла; 0 = ещё не выбран (см. app.PickPort).
	Port int `json:"port,omitempty"`
}

// addNetwork добавляет сеть в список без дублей (по имени). Вызывать под cfgMu.
func (c *Config) addNetwork(name, password, discovery string) {
	for i, p := range c.Networks {
		if p.Name == name {
			c.Networks[i].Password = password
			c.Networks[i].Discovery = discovery
			return
		}
	}
	c.Networks = append(c.Networks, NetProfile{Name: name, Password: password, Discovery: discovery})
}

// removeNetworkByTag убирает из списка сеть с данным hex-тегом. Вызывать под cfgMu.
func (c *Config) removeNetworkByTag(tag string) {
	out := c.Networks[:0]
	for _, p := range c.Networks {
		if netTag(p) != tag {
			out = append(out, p)
		}
	}
	c.Networks = out
}

// sendLogs — значение с учётом умолчания (включено, если не задано явно).
func (c Config) sendLogs() bool { return c.SendLogs == nil || *c.SendLogs }

// portMap — значение с учётом умолчания (включено, если не задано явно).
func (c Config) portMap() bool { return c.PortMap == nil || *c.PortMap }

// effectiveSignals — список сигналок с учётом умолчания.
func effectiveSignals(c Config) []string {
	if len(c.Signals) > 0 {
		return c.Signals
	}
	return defaultSignalURLs
}

// effectiveRelay — адрес ретранслятора с учётом умолчания.
func effectiveRelay(c Config) string {
	if c.Relay != nil {
		return *c.Relay
	}
	return defaultRelayAddr
}

// --- конфиг -----------------------------------------------------------------

func configFilePath() string {
	dir, err := app.ConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "lanmesh", "config.json")
}

func loadConfig() Config {
	var c Config
	data, err := os.ReadFile(configFilePath())
	if err == nil {
		json.Unmarshal(data, &c)
	}
	// Миграция со старого одно-сетевого конфига: если список сетей пуст, но есть
	// сохранённая сеть с паролем (Remember) — переносим её в список.
	if len(c.Networks) == 0 && c.Network != "" && c.Password != "" && c.Remember {
		c.Networks = []NetProfile{{Name: c.Network, Password: c.Password}}
	}
	// Legacy-поля больше не нужны — список сетей теперь единственный источник.
	c.Network, c.Password, c.Remember = "", "", false
	return c
}

func saveConfig(c Config) {
	path := configFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("config mkdir: %v", err)
		return
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("config write: %v", err)
	}
}
