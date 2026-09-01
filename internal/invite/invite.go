// Package invite — формат ссылки-приглашения в сеть и его разбор.
//
// Ссылка выглядит так:
//
//	lanmesh://join?net=<имя>&pass=<пароль>&sig=<сигналка>&sig=<сигналка>&relay=<адрес>
//	lanmesh://join?net=<имя>&pass=<пароль>&disc=dht
//
// Зачем отдельный пакет: формат один, а пользуются им трое — панель его строит
// (handleInvite), панель же разбирает вставленную ссылку, и консольный клиент
// разбирает её во флаге -invite. Разъехавшиеся реализации означали бы, что друг
// по одной и той же ссылке попадает в РАЗНЫЕ сети (ключ выводится из имени,
// пароля и режима обнаружения) и не понимает, почему никого не видит.
//
// Схема lanmesh:// в системе НЕ регистрируется: клик по ссылке ничего не
// откроет, её копируют и вставляют. Префикс здесь — просто узнаваемая упаковка.
//
// БЕЗОПАСНОСТЬ: ссылка несёт пароль сети открытым текстом. Иначе нельзя —
// ключ сети есть KDF от имени и пароля, приглашать больше нечем. Значит,
// передавать её можно только по доверенному каналу.
package invite

import (
	"errors"
	"net/url"
	"strings"
)

// Scheme — префикс ссылки-приглашения.
const Scheme = "lanmesh://join?"

// Режимы обнаружения, которые может нести приглашение. Совпадают со значениями
// app.Discovery* — дублируются здесь, чтобы пакет не зависел от internal/app
// (иначе получился бы цикл: app → invite → app).
const (
	DiscoverySignal   = "signal"
	DiscoveryDHT      = "dht"
	DiscoveryDHTRelay = "dht+relay"
)

// Invite — разобранное приглашение.
type Invite struct {
	Network  string
	Password string
	// Discovery — способ обнаружения. Пусто = в ссылке не указан (обычные
	// сигналки). Он вшит в ключ сети, поэтому приглашение обязано его нести:
	// выбрать «свой» режим значит попасть в другую сеть.
	Discovery string
	Signals   []string
	// Relay — nil, если в ссылке поля не было; пустая строка — ЯВНОЕ «без
	// ретранслятора». Различать обязательно: отсутствие поля means «решай сам»,
	// а пустое значение — осознанный выбор пригласившего.
	Relay *string
}

// Build собирает ссылку-приглашение. relay==nil — поле не добавляется.
func Build(in Invite) string {
	q := url.Values{}
	q.Set("net", in.Network)
	q.Set("pass", in.Password)
	switch in.Discovery {
	case DiscoveryDHT, DiscoveryDHTRelay:
		// У сети без серверов сигналок нет вовсе; ретранслятор кладём, только
		// если он ей разрешён — решает вызывающий, передав его в Relay.
		q.Set("disc", in.Discovery)
	default:
		for _, s := range in.Signals {
			q.Add("sig", s)
		}
	}
	if in.Relay != nil {
		q.Set("relay", *in.Relay)
	}
	return Scheme + q.Encode()
}

// Parse разбирает ссылку-приглашение. Принимает как полную ссылку, так и одну
// query-часть без префикса — люди копируют по-разному, а требовать точности от
// текста, прошедшего через мессенджер, значит ловить жалобы на ровном месте.
//
// Разбор пары за парой, а не url.ParseQuery целиком: одна битая
// %-последовательность (мессенджер порезал ссылку) не должна обнулять всё
// приглашение — лучше взять то, что распозналось, и честно сказать, чего не
// хватает.
func Parse(s string) (Invite, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Invite{}, errors.New("пустая ссылка")
	}
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[i+1:]
	}

	var in Invite
	for _, part := range strings.Split(s, "&") {
		eq := strings.Index(part, "=")
		if eq < 0 {
			continue
		}
		k := part[:eq]
		v, err := url.QueryUnescape(part[eq+1:])
		if err != nil {
			continue // битую пару пропускаем, остальные разбираем
		}
		switch k {
		case "net":
			in.Network = v
		case "pass":
			in.Password = v
		case "sig":
			if v != "" {
				in.Signals = append(in.Signals, v)
			}
		case "relay":
			relay := v
			in.Relay = &relay
		case "disc":
			// Незнакомое значение читаем как обычные сигналки, а не как ошибку:
			// так ссылка от будущей версии с новым режимом хотя бы попробует
			// подключиться привычным способом.
			if v == DiscoveryDHT || v == DiscoveryDHTRelay {
				in.Discovery = v
			} else {
				in.Discovery = DiscoverySignal
			}
		}
	}

	switch {
	case in.Network == "" && in.Password == "":
		return in, errors.New("это не похоже на приглашение: нет ни имени сети, ни пароля")
	case in.Network == "":
		return in, errors.New("в приглашении нет имени сети (net)")
	case in.Password == "":
		return in, errors.New("в приглашении нет пароля (pass)")
	}
	return in, nil
}
