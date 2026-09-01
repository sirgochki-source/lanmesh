package invite

import "testing"

func strptr(s string) *string { return &s }

// Круговой прогон: то, что собрали, обязано разобраться обратно без потерь.
// Это главный инвариант пакета — панель строит ссылку, а разбирают её и панель,
// и консольный клиент; разъехавшись, они увели бы друга в другую сеть.
func TestBuildParseRoundTrip(t *testing.T) {
	cases := map[string]Invite{
		"обычная сеть": {
			Network:  "myteam",
			Password: "123",
			Signals:  []string{"https://a.example", "https://b.example:25557"},
			Relay:    strptr("relay.example:25555"),
		},
		"без ретранслятора (явно)": {
			Network: "team", Password: "pw", Signals: []string{"https://a.example"}, Relay: strptr(""),
		},
		"DHT без серверов": {
			Network: "team", Password: "pw", Discovery: DiscoveryDHT,
		},
		"DHT с релеем": {
			Network: "team", Password: "pw", Discovery: DiscoveryDHTRelay, Relay: strptr("r.example:1"),
		},
		"спецсимволы в имени и пароле": {
			Network: "моя сеть & её+друзья", Password: "п=а&р?о ль%20", Signals: []string{"https://a.example"},
			Relay: strptr(""),
		},
	}
	for name, in := range cases {
		got, err := Parse(Build(in))
		if err != nil {
			t.Fatalf("%s: разбор собранной ссылки не удался: %v", name, err)
		}
		if got.Network != in.Network || got.Password != in.Password {
			t.Fatalf("%s: имя/пароль исказились: %+v", name, got)
		}
		if len(got.Signals) != len(in.Signals) {
			t.Fatalf("%s: сигналок было %d, стало %d", name, len(in.Signals), len(got.Signals))
		}
		for i := range in.Signals {
			if got.Signals[i] != in.Signals[i] {
				t.Fatalf("%s: сигналка %d: было %q, стало %q", name, i, in.Signals[i], got.Signals[i])
			}
		}
		if (in.Relay == nil) != (got.Relay == nil) {
			t.Fatalf("%s: потеряно различие «поля не было» / «поле пустое»: было %v, стало %v",
				name, in.Relay, got.Relay)
		}
		if in.Relay != nil && *got.Relay != *in.Relay {
			t.Fatalf("%s: ретранслятор: было %q, стало %q", name, *in.Relay, *got.Relay)
		}
		// Режим у обычной сети в ссылке отсутствует и разбирается как пустой —
		// вызывающий трактует пусто как «обычные сигналки».
		if in.Discovery != "" && got.Discovery != in.Discovery {
			t.Fatalf("%s: режим: было %q, стало %q", name, in.Discovery, got.Discovery)
		}
	}
}

// Разница между «поля relay не было» и «relay пустой» — не педантизм: первое
// значит «решай сам» (клиент возьмёт своё умолчание), второе — осознанное
// решение пригласившего работать без ретранслятора. Слив их, мы бы навязали
// человеку чужой сервер или, наоборот, лишили запасного пути.
func TestRelayAbsentVsEmpty(t *testing.T) {
	absent, err := Parse("lanmesh://join?net=a&pass=b")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if absent.Relay != nil {
		t.Fatalf("поля relay не было, а разобралось как %q", *absent.Relay)
	}
	empty, err := Parse("lanmesh://join?net=a&pass=b&relay=")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if empty.Relay == nil {
		t.Fatal("relay= (пустой) обязан отличаться от отсутствующего поля")
	}
	if *empty.Relay != "" {
		t.Fatalf("relay= разобрался как %q, ожидали пустую строку", *empty.Relay)
	}
}

// Края разбора: без префикса, мусорный режим, битая %-последовательность,
// отсутствие обязательных полей.
func TestParseEdges(t *testing.T) {
	// Голая query без префикса — люди копируют ссылку из мессенджера по-разному.
	if in, err := Parse("net=a&pass=b"); err != nil || in.Network != "a" || in.Password != "b" {
		t.Fatalf("голая query не разобралась: %+v %v", in, err)
	}
	// Незнакомый режим читается как обычные сигналки, а не как отказ: ссылка от
	// будущей версии должна хотя бы попробовать подключиться.
	if in, _ := Parse("net=a&pass=b&disc=quantum"); in.Discovery != DiscoverySignal {
		t.Fatalf("незнакомый режим разобран как %q, ожидали %q", in.Discovery, DiscoverySignal)
	}
	// Битая пара не должна обнулять всё приглашение.
	in, err := Parse("net=a&pass=b&sig=%zz&relay=r:1")
	if err != nil {
		t.Fatalf("битая пара уронила разбор целиком: %v", err)
	}
	if in.Network != "a" || in.Password != "b" || in.Relay == nil || *in.Relay != "r:1" {
		t.Fatalf("после битой пары остальное разобралось неверно: %+v", in)
	}
	if len(in.Signals) != 0 {
		t.Fatalf("битая сигналка попала в список: %+v", in.Signals)
	}
	// Обязательные поля.
	for _, bad := range []string{"", "net=a", "pass=b", "поехали"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("ссылка %q обязана быть отвергнута", bad)
		}
	}
}
