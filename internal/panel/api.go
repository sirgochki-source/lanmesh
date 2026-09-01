package panel

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/app"
	"github.com/sirgochki-source/lanmesh/internal/invite"
)


func (p *Panel) handleState(w http.ResponseWriter, r *http.Request) {
	st := p.sess.State()

	// savedNet — сохранённая сеть для показа неактивной карточкой, когда узел отключён
	// (сеть не пропадает из списка, а становится серой). Тег совпадает с NetworkView.Tag
	// у активной сети — фронт по нему сопоставляет активную/неактивную.
	type savedNet struct {
		Name      string `json:"name"`
		Tag       string `json:"tag"`
		Discovery string `json:"discovery,omitempty"`
	}
	p.cfgMu.Lock()
	sendLogs := p.cfg.sendLogs()
	portMapOn := p.cfg.portMap()
	cfgName := p.cfg.SelfName // своё имя из конфига (пусто = используется hostname)
	savedList := make([]savedNet, 0, len(p.cfg.Networks))
	for _, np := range p.cfg.Networks {
		savedList = append(savedList, savedNet{Name: np.Name, Tag: netTag(np), Discovery: np.Discovery})
	}
	cfgSignals := append([]string(nil), p.cfg.Signals...)
	cfgRelay := ""
	if p.cfg.Relay != nil {
		cfgRelay = *p.cfg.Relay
	}
	p.cfgMu.Unlock()

	// cfgSignals/cfgRelay — СВОИ (кастомные) адреса пользователя для показа и правки в
	// настройках (по явной просьбе). Пусто = используются стандартные серверы. Дефолтные
	// адреса (плейсхолдеры) намеренно не раскрываем — управляем только своими.
	out := struct {
		app.StateView
		SendLogs       bool       `json:"sendLogs"`
		PortMapEnabled bool       `json:"portMapEnabled"` // чекбокс — состояние из конфига, как sendLogs
		Portmap        string     `json:"portmap"`        // строка статуса проброса (см. Session.PortmapStatus)
		IPv6           bool       `json:"ipv6"`           // есть ли свой маршрутизируемый IPv6 (портмапу тогда не нужен)
		SavedNetworks  int        `json:"savedNetworks"`
		SavedNets      []savedNet `json:"savedNets"`
		CfgSignals     []string   `json:"cfgSignals"`
		CfgRelay       string     `json:"cfgRelay"`
		CfgName        string     `json:"cfgName"`
		Version        string     `json:"version"`
		// CanUpdate — есть ли у этой сборки автообновление. Оно тянет .exe и
		// существует только под Windows: маршруты /api/checkupdate и /api/update
		// регистрирует сам GUI, поэтому на Linux кнопку рисовать нельзя — она
		// вела бы в 404.
		CanUpdate bool `json:"canUpdate"`
	}{
		StateView: st, SendLogs: sendLogs, PortMapEnabled: portMapOn, Portmap: p.sess.PortmapStatus(), IPv6: app.HasGlobalIPv6(),
		SavedNetworks: len(savedList), SavedNets: savedList, CfgSignals: cfgSignals, CfgRelay: cfgRelay,
		CfgName: cfgName, Version: p.version, CanUpdate: p.canUpdate,
	}
	writeJSON(w, out, http.StatusOK)
}

// handleAddNetwork присоединяет сеть (и запоминает её в списке для автоподключения).
// Мультисеть: если уже есть другие сети, эта добавляется к ним, а не заменяет.
func (p *Panel) handleAddNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Network  string   `json:"network"`
		Password string   `json:"password"`
		Signals  []string `json:"signals"` // из приглашения; пусто = не трогать
		Relay    *string  `json:"relay"`   // из приглашения; nil = не трогать, "" = без релея
		// DHT — искать участников через публичную DHT вместо сигналок.
		DHT bool `json:"dht"`
		// DHTRelay — разрешить такой сети ретранслятор как запасной путь. Учитывается
		// только вместе с DHT; вшивается в ключ сети, поэтому одинаков у всех.
		DHTRelay bool `json:"dhtRelay"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}
	req.Network = strings.TrimSpace(req.Network)
	if req.Network == "" || req.Password == "" {
		writeJSON(w, map[string]string{"error": "нужны имя сети и пароль"}, http.StatusBadRequest)
		return
	}

	mode := app.DiscoverySignal
	if req.DHT {
		mode = app.DiscoveryDHT
		if req.DHTRelay {
			mode = app.DiscoveryDHTRelay
		}
	}

	// Серверы из приглашения принимаем ДО поднятия узла (на ходу их менять нельзя).
	// Если вход в сеть затем не удастся — откатываем, чтобы не остаться с чужими
	// серверами без самой сети. Для DHT-сети серверов из приглашения не бывает:
	// смысл режима в том, чтобы их не было вовсе.
	var note string
	var revert func()
	switch mode {
	case app.DiscoverySignal:
		note, revert = p.applyInviteServers(req.Signals, req.Relay)
	case app.DiscoveryDHTRelay:
		// Сигналок у сети нет, а вот ретранслятор должен быть ТОТ ЖЕ, что у
		// пригласившего: релей сводит пиров по паре (тег, peerID), и на разных
		// серверах они друг друга не найдут.
		note, revert = p.applyInviteServers(nil, req.Relay)
	}

	if err := p.AddNetwork(req.Network, req.Password, mode); err != nil {
		if revert != nil {
			revert()
		}
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusInternalServerError)
		return
	}

	resp := map[string]any{"ok": true}
	if note != "" {
		resp["note"] = note
	}
	writeJSON(w, resp, http.StatusOK)
}

// applyInviteServers принимает сигналки/релей из приглашения, чтобы друг попал в те
// же серверы, что и пригласивший. Возвращает заметку для показа (""=молча приняли).
//
// Правила осторожные: (1) меняем только пока узел снят — на ходу подмена сигналок
// это гонка, да и уже поднятые сети разъедутся; (2) если у друга уже свои кастомные
// серверы — НЕ перетираем их (это его выбор и общая настройка всех его сетей),
// только предупреждаем. На чистом клиенте (дефолты) — просто принимаем.
// Возвращает заметку для показа и функцию отката (nil, если менять было нечего) —
// вызывающий откатывает, если вход в сеть после смены серверов не удался.
func (p *Panel) applyInviteServers(rawSignals []string, relay *string) (string, func()) {
	var sigs []string
	for _, u := range rawSignals {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			continue
		}
		sigs = append(sigs, u)
	}
	wantSignals := len(sigs) > 0
	wantRelay := relay != nil
	if !wantSignals && !wantRelay {
		return "", nil
	}

	p.cfgMu.Lock()
	c := p.cfg
	p.cfgMu.Unlock()

	newSignals := effectiveSignals(c)
	if wantSignals {
		newSignals = sigs
	}
	newRelay := effectiveRelay(c)
	if wantRelay {
		newRelay = *relay
	}
	// Уже такие же — ничего делать не нужно (и не поднимаем шум).
	if sameStrings(newSignals, effectiveSignals(c)) && newRelay == effectiveRelay(c) {
		return "", nil
	}
	if len(c.Signals) > 0 || c.Relay != nil {
		return "у тебя настроены свои серверы — из приглашения их не менял", nil
	}

	if err := p.sess.SetSignalURLs(newSignals); err != nil {
		return "чтобы принять серверы из приглашения, сначала отключись от сетей", nil
	}
	p.sess.UseRelay(newRelay)

	// Прежнее состояние для возможного отката.
	prevSignals, prevRelay := c.Signals, c.Relay
	prevEffSignals, prevEffRelay := effectiveSignals(c), effectiveRelay(c)

	p.cfgMu.Lock()
	if wantSignals {
		p.cfg.Signals = sigs
	}
	if wantRelay {
		rr := *relay
		p.cfg.Relay = &rr
	}
	saveConfig(p.cfg)
	p.cfgMu.Unlock()
	log.Printf("серверы приняты из приглашения: сигналок %d, relay %q", len(newSignals), newRelay)

	revert := func() {
		_ = p.sess.SetSignalURLs(prevEffSignals) // узел на этот момент снят — не упадёт
		p.sess.UseRelay(prevEffRelay)
		p.cfgMu.Lock()
		p.cfg.Signals, p.cfg.Relay = prevSignals, prevRelay
		saveConfig(p.cfg)
		p.cfgMu.Unlock()
		log.Printf("серверы из приглашения откачены (вход в сеть не удался)")
	}
	return "", revert
}

// sameStrings — равны ли два списка по порядку и составу.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// handleLeaveNetwork выходит из сети по её тегу и убирает её из сохранённого списка.
func (p *Panel) handleLeaveNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}
	req.Tag = strings.TrimSpace(req.Tag)
	raw, err := hex.DecodeString(req.Tag)
	if err != nil || len(raw) != 32 {
		writeJSON(w, map[string]string{"error": "неверный тег"}, http.StatusBadRequest)
		return
	}
	var tagB [32]byte
	copy(tagB[:], raw)
	p.sess.RemoveNetwork(tagB)

	p.cfgMu.Lock()
	p.cfg.removeNetworkByTag(req.Tag)
	saveConfig(p.cfg)
	p.cfgMu.Unlock()

	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}

// handleSendLogs включает/выключает отправку диагностики. Действует сразу и
// переживает перезапуск (пишется в конфиг).
func (p *Panel) handleSendLogs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}

	p.sess.SetLogUpload(req.Enabled)

	p.cfgMu.Lock()
	v := req.Enabled
	p.cfg.SendLogs = &v
	saveConfig(p.cfg)
	p.cfgMu.Unlock()

	if req.Enabled {
		log.Printf("отправка диагностики на сигналку включена")
	} else {
		log.Printf("отправка диагностики на сигналку выключена")
	}
	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}

// handlePortMap включает/выключает проброс порта на роутере. Выключение
// действует немедленно (снимается маппинг и правило брандмауэра); включение
// на уже поднятом узле применится только со следующим переподключением — см.
// комментарий у Session.SetPortMap.
func (p *Panel) handlePortMap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}

	p.sess.SetPortMap(req.Enabled)

	p.cfgMu.Lock()
	v := req.Enabled
	p.cfg.PortMap = &v
	saveConfig(p.cfg)
	p.cfgMu.Unlock()

	if req.Enabled {
		log.Printf("проброс порта включён (применится при следующем подключении)")
	} else {
		log.Printf("проброс порта выключен")
	}
	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}

// handleSendDiag немедленно заливает диагностику (лог + свежий снимок) на сигналки
// и возвращает тег сети — по нему её читают через /logs?net=<тег>.
func (p *Panel) handleSendDiag(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tag, err := p.sess.SendDiagnostics(ctx)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error(), "tag": tag}, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"ok": "1", "tag": tag}, http.StatusOK)
}

// handleDiagnose гоняет пробу окружения (тип NAT, VPN-перехват, egress) и отдаёт
// её для показа в панели. Работает и без поднятой сети.
func (p *Panel) handleDiagnose(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, p.sess.Diagnose(), http.StatusOK)
}

func (p *Panel) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	// Ответ отдаём и флашим ДО снятия адаптера: его закрытие ненадолго трогает
	// сетевой стек и может оборвать ещё не доставленный ответ.
	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	p.sess.Stop()
}

// handleReconnect переподнимает все сохранённые сети — пара к handleDisconnect. Даёт
// «Подключиться» после «Отключиться» без выхода из сетей и без перезапуска приложения
// (AddNetwork после Stop сам поднимает узел заново).
func (p *Panel) handleReconnect(w http.ResponseWriter, r *http.Request) {
	errs := p.Reconnect()
	if len(errs) > 0 {
		writeJSON(w, map[string]string{"error": strings.Join(errs, "; ")}, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}

// Reconnect переподнимает все сохранённые сети и возвращает ошибки по каждой.
// Экспортирован ради трея GUI: он делал ровно то же самое своей копией цикла, и
// копия успела разъехаться — звала AddNetwork вместо AddNetworkMode, то есть
// молча теряла режим обнаружения и уводила DHT-сети в обычные сигналки.
func (p *Panel) Reconnect() []string {
	p.cfgMu.Lock()
	nets := append([]NetProfile(nil), p.cfg.Networks...)
	p.cfgMu.Unlock()

	var errs []string
	for _, np := range nets {
		if err := p.sess.AddNetworkMode(np.Name, np.Password, np.Discovery); err != nil {
			errs = append(errs, np.Name+": "+err.Error())
		}
	}
	return errs
}

// handleSetName задаёт своё отображаемое имя узла (пусто = вернуться к hostname). Имя
// сохраняется в конфиг и применяется к сеансу; пиры увидят его при следующем анонсе.
func (p *Panel) handleSetName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if rs := []rune(name); len(rs) > 40 { // не гнать пирам простыню
		name = string(rs[:40])
	}
	p.cfgMu.Lock()
	p.cfg.SelfName = name
	saveConfig(p.cfg)
	p.cfgMu.Unlock()
	p.sess.SetName(name)
	writeJSON(w, map[string]any{"ok": true, "name": name}, http.StatusOK)
}

// handleSettings читает и меняет список серверов (сигналки + relay). Менять можно
// ТОЛЬКО пока сеть снята: registerLoop берёт снимок сигналок при старте.
func (p *Panel) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		p.cfgMu.Lock()
		c := p.cfg
		p.cfgMu.Unlock()
		// Сами адреса серверов НЕ отдаём в панель — чтобы личные сигналки/релей не
		// светились в UI (тултипы, поля настроек). Только метаданные: сколько
		// настроено и свои это или стандартные. Ввести кастомные всё равно можно.
		writeJSON(w, map[string]any{
			"custom":      len(c.Signals) > 0 || c.Relay != nil,
			"signalCount": len(effectiveSignals(c)),
			"hasRelay":    effectiveRelay(c) != "",
		}, http.StatusOK)
		return
	}

	var req struct {
		Signals []string `json:"signals"`
		Relay   string   `json:"relay"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "bad json"}, http.StatusBadRequest)
		return
	}

	// Пустые строки/пробелы выкидываем; валидируем схему — иначе клиент молча
	// не достучится до кривого адреса и будет думать, что «сигналка лежит».
	var sigs []string
	for _, u := range req.Signals {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeJSON(w, map[string]string{"error": "сигналка должна начинаться с http:// или https:// — " + u}, http.StatusBadRequest)
			return
		}
		sigs = append(sigs, u)
	}
	relay := strings.TrimSpace(req.Relay)

	// Пустой список сигналок = вернуться к дефолту, а не остаться без связи вовсе.
	applySignals := sigs
	if len(applySignals) == 0 {
		applySignals = defaultSignalURLs
	}
	applyRelay := relay
	if applyRelay == "" {
		applyRelay = defaultRelayAddr
	}

	if err := p.sess.SetSignalURLs(applySignals); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusConflict)
		return
	}
	p.sess.UseRelay(applyRelay)

	p.cfgMu.Lock()
	if len(sigs) == 0 {
		p.cfg.Signals = nil // omitempty => чистый конфиг = дефолт
	} else {
		p.cfg.Signals = sigs
	}
	if relay == "" {
		p.cfg.Relay = nil
	} else {
		p.cfg.Relay = &relay
	}
	saveConfig(p.cfg)
	p.cfgMu.Unlock()

	log.Printf("настройки серверов обновлены: сигналок %d, relay %q", len(applySignals), applyRelay)
	writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}

// handleInvite отдаёт ссылку-приглашение lanmesh://join?net=…&pass=…&sig=…&relay=…
// для сети с указанным тегом (?tag=<hex>). Имя+пароль берём из сохранённого списка.
//
// В ссылку ВСЕГДА кладём наши эффективные сигналки/релей (и кастомные, и дефолтные):
// чтобы попасть в ту же сеть, друг должен ходить в те же серверы. Дублируются они с
// его настройками или нет — разбирается уже клиент при входе (applyInviteServers).
// Дефолты и так вшиты в его бинарь, так что раскрытие адресов тут ничего не добавляет.
func (p *Panel) handleInvite(w http.ResponseWriter, r *http.Request) {
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))

	p.cfgMu.Lock()
	c := p.cfg
	var name, pass, disc string
	for _, p := range p.cfg.Networks {
		if tag == "" || netTag(p) == tag {
			name, pass, disc = p.Name, p.Password, p.Discovery
			break
		}
	}
	p.cfgMu.Unlock()

	if name == "" {
		writeJSON(w, map[string]string{"link": "", "note": "сеть не найдена"}, http.StatusOK)
		return
	}
	// Формат ссылки живёт в internal/invite — там же его разбирают панель и
	// консольный клиент (флаг -invite). Держать сборку здесь значило бы иметь две
	// реализации одного формата, а разъехавшись, они увели бы друга в другую сеть.
	in := invite.Invite{Network: name, Password: pass, Discovery: disc}
	if disc == app.DiscoveryDHT || disc == app.DiscoveryDHTRelay {
		// Сигналок у сети без серверов нет; ретранслятор кладём, только если он
		// ей вообще разрешён.
		if disc == app.DiscoveryDHTRelay {
			r := effectiveRelay(c)
			in.Relay = &r
		}
	} else {
		in.Signals = effectiveSignals(c)
		r := effectiveRelay(c) // "" — осознанно «без релея»
		in.Relay = &r
	}
	writeJSON(w, map[string]string{"link": invite.Build(in)}, http.StatusOK)
}

// AddNetwork подключает сеть и запоминает её в конфиге для автоподключения.
// Экспортирован ради консольного клиента с -panel: он принимает сеть флагами, и
// без этого ему пришлось бы повторить связку «подключить + сохранить» своей
// копией — а копии расходятся (см. Reconnect).
func (p *Panel) AddNetwork(name, password, mode string) error {
	if err := p.sess.AddNetworkMode(name, password, mode); err != nil {
		return err
	}
	p.cfgMu.Lock()
	p.cfg.addNetwork(name, password, mode)
	saveConfig(p.cfg)
	p.cfgMu.Unlock()
	return nil
}
