//go:build windows

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// staticHandler должен отдавать встроенные ассеты панели (web/) с корректными
// MIME-типами — иначе WebView2 (строгая проверка MIME для ES-модулей) не
// исполнит app.js/модули и панель окажется пустой.
func TestStaticHandlerServesEmbeddedAssets(t *testing.T) {
	srv := httptest.NewServer(staticHandler())
	defer srv.Close()

	cases := []struct {
		path, wantCT, wantSub string
	}{
		{"/", "text/html", `<div id="root"`},
		{"/app.css", "text/css", "Command Glass"},
		{"/app.js", "text/javascript", "renderView"},
		{"/views/list.js", "text/javascript", "peerRowCompact"},
		{"/views/shell.js", "text/javascript", "statusPill"},
		{"/lib/sanitize.js", "text/javascript", "dispName"},
		{"/lib/sparkline.js", "text/javascript", "sparklineSVG"},
	}
	for _, c := range cases {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatalf("GET %s: %v", c.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: статус %d, ожидали 200", c.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, c.wantCT) {
			t.Errorf("GET %s: Content-Type %q, ожидали префикс %q", c.path, ct, c.wantCT)
		}
		if !strings.Contains(string(body), c.wantSub) {
			t.Errorf("GET %s: в теле нет ожидаемого фрагмента %q", c.path, c.wantSub)
		}
	}
}

// Несуществующий ассет должен отдавать 404, а не панику/500.
func TestStaticHandlerMissing(t *testing.T) {
	srv := httptest.NewServer(staticHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/nope-does-not-exist.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("несуществующий путь: статус %d, ожидали 404", resp.StatusCode)
	}
}
