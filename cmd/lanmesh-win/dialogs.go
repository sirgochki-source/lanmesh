//go:build windows

package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// promptAddNetwork — модальный диалог: имя+пароль (или вставленная ссылка-инвайт).
func promptAddNetwork() (name, pass string, ok bool) {
	var dlg *walk.Dialog
	var nameEd, passEd, inviteEd *walk.LineEdit
	var okPB, cancelPB *walk.PushButton

	Dialog{
		AssignTo:      &dlg,
		Title:         "Добавить сеть",
		MinSize:       Size{Width: 380, Height: 230},
		DefaultButton: &okPB,
		CancelButton:  &cancelPB,
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: "Приглашение — вставь ссылку от друга, поля заполнятся сами:"},
			LineEdit{AssignTo: &inviteEd, OnTextChanged: func() {
				n, p := parseInvite(inviteEd.Text())
				if n != "" {
					nameEd.SetText(n)
				}
				if p != "" {
					passEd.SetText(p)
				}
			}},
			Label{Text: "Имя сети"},
			LineEdit{AssignTo: &nameEd},
			Label{Text: "Пароль"},
			LineEdit{AssignTo: &passEd, PasswordMode: true},
			VSpacer{Size: 6},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &okPB, Text: "Добавить", OnClicked: func() {
						n, p := strings.TrimSpace(nameEd.Text()), passEd.Text()
						if n == "" || p == "" {
							walk.MsgBox(dlg, "lanmesh", "Нужны имя сети и пароль.", walk.MsgBoxIconWarning)
							return
						}
						name, pass, ok = n, p, true
						dlg.Accept()
					}},
					PushButton{AssignTo: &cancelPB, Text: "Отмена", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(mw)
	return
}

// parseInvite достаёт имя и пароль из ссылки lanmesh://join?net=…&pass=…
func parseInvite(s string) (netName, pass string) {
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil {
		q := u.Query()
		return q.Get("net"), q.Get("pass")
	}
	return "", ""
}

// onSettings — диалог серверов. Адреса скрыты; можно вписать свои. Менять можно
// только когда узел снят (нет сетей): SetSignalURLs на ходу — гонка.
func onSettings() {
	up := sess.State().Running

	cfgMu.Lock()
	nSig := len(effSignals(cfg))
	hasRelay := effRelay(cfg) != ""
	custom := len(cfg.Signals) > 0 || cfg.Relay != nil
	cfgMu.Unlock()

	kind := "стандартные"
	if custom {
		kind = "свои"
	}
	relayStr := "без релея"
	if hasRelay {
		relayStr = "релей задан"
	}
	status := fmt.Sprintf("Настроено: %d сигналок, %s (%s). Адреса скрыты.", nSig, relayStr, kind)

	note := "Оставь поля пустыми — ничего не изменится. Впиши свои адреса, чтобы переопределить."
	if up {
		note = "Отключись от всех сетей (выйди из каждой), чтобы менять серверы."
	}

	var dlg *walk.Dialog
	var sigEd *walk.TextEdit
	var relayEd *walk.LineEdit
	var okPB, cancelPB *walk.PushButton

	Dialog{
		AssignTo:      &dlg,
		Title:         "Серверы",
		MinSize:       Size{Width: 460, Height: 300},
		DefaultButton: &okPB,
		CancelButton:  &cancelPB,
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: status},
			Label{Text: "Свои сигналки (по одной ссылке на строку):"},
			TextEdit{AssignTo: &sigEd, MinSize: Size{Width: 0, Height: 70}, Enabled: !up},
			Label{Text: "Ретранслятор (host:port):"},
			LineEdit{AssignTo: &relayEd, Enabled: !up},
			Label{Text: note},
			VSpacer{Size: 4},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &okPB, Text: "Сохранить", Enabled: !up, OnClicked: func() {
						var sigs []string
						for _, ln := range strings.Split(sigEd.Text(), "\n") {
							ln = strings.TrimSpace(ln)
							if ln == "" {
								continue
							}
							if !strings.HasPrefix(ln, "http://") && !strings.HasPrefix(ln, "https://") {
								walk.MsgBox(dlg, "lanmesh", "Сигналка должна начинаться с http:// или https://\n"+ln, walk.MsgBoxIconWarning)
								return
							}
							sigs = append(sigs, ln)
						}
						relay := strings.TrimSpace(relayEd.Text())
						if len(sigs) == 0 && relay == "" {
							walk.MsgBox(dlg, "lanmesh", "Впиши свои адреса, чтобы переопределить.", walk.MsgBoxIconInformation)
							return
						}
						cfgMu.Lock()
						if len(sigs) > 0 {
							cfg.Signals = sigs
						}
						if relay != "" {
							r := relay
							cfg.Relay = &r
						}
						c := cfg
						cfgMu.Unlock()
						if err := sess.SetSignalURLs(effSignals(c)); err != nil {
							walk.MsgBox(dlg, "lanmesh", err.Error(), walk.MsgBoxIconError)
							return
						}
						sess.UseRelay(effRelay(c))
						saveConfig()
						dlg.Accept()
					}},
					PushButton{AssignTo: &cancelPB, Text: "Закрыть", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(mw)
}
