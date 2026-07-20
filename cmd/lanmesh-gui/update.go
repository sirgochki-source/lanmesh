//go:build windows

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// version — текущая версия сборки. Бампается вручную под каждый релиз (совпадает с git-тегом).
const version = "v0.6.1"

// Обновление тянем через ОБЫЧНЫЙ github.com, а НЕ api.github.com: у API жёсткий лимит
// 60 запросов/час на IP (за CGNAT — общий на всех, отсюда 403). Страница /releases/latest
// редиректит на /releases/tag/<тег> — тег берём из редиректа; ассет — по стабильному URL
// /releases/latest/download/<имя>. И то, и другое — не API, лимитов практически нет.
const (
	githubLatestURL = "https://github.com/sirgochki-source/lanmesh/releases/latest"
	githubAssetURL  = "https://github.com/sirgochki-source/lanmesh/releases/latest/download/lanmesh-gui.exe"
)

// latestTag возвращает тег последнего релиза, читая Location редиректа /releases/latest.
func latestTag() (string, error) {
	cl := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequest("GET", githubLatestURL, nil)
	req.Header.Set("User-Agent", "lanmesh-gui")
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("нет релизов (HTTP %d)", resp.StatusCode)
	}
	i := strings.LastIndex(loc, "/tag/")
	if i < 0 {
		return "", fmt.Errorf("не удалось разобрать тег")
	}
	return strings.TrimSpace(loc[i+len("/tag/"):]), nil
}

// parseVer разбирает "vMAJOR.MINOR.PATCH" в [3]int (нецифровой хвост октета отбрасывается).
func parseVer(s string) [3]int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var v [3]int
	for i, part := range strings.SplitN(s, ".", 3) {
		num := ""
		for _, r := range part {
			if r < '0' || r > '9' {
				break
			}
			num += string(r)
		}
		v[i], _ = strconv.Atoi(num)
	}
	return v
}

// isNewer — latest строго новее current (посегментно major→minor→patch)?
func isNewer(current, latest string) bool {
	c, l := parseVer(current), parseVer(latest)
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// handleCheckUpdate: GET — сравнивает текущую версию с последним релизом GitHub.
func handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	latest, err := latestTag()
	if err != nil {
		writeJSON(w, map[string]string{"error": "не удалось проверить: " + err.Error()}, http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"current":   version,
		"latest":    latest,
		"hasUpdate": isNewer(version, latest),
	}, http.StatusOK)
}

// handleUpdate: POST — скачивает новый lanmesh-gui.exe, заменяет текущий и перезапускается.
func handleUpdate(w http.ResponseWriter, r *http.Request) {
	latest, err := latestTag()
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusBadGateway)
		return
	}
	if !isNewer(version, latest) {
		writeJSON(w, map[string]bool{"ok": true, "upToDate": true}, http.StatusOK)
		return
	}
	if err := selfUpdate(); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()}, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true, "restarting": true}, http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go restartAfterUpdate()
}

// selfUpdate скачивает новый exe рядом с текущим и переставляет его на место. Windows
// позволяет ПЕРЕИМЕНОВАТЬ запущенный exe (но не перезаписать/удалить), поэтому текущий
// уводим в .old, а скачанный ставим на его путь. .old чистится на следующем старте.
func selfUpdate() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.Abs(exe)

	req, _ := http.NewRequest("GET", githubAssetURL, nil)
	req.Header.Set("User-Agent", "lanmesh-gui")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("скачивание: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("скачивание: %s", resp.Status)
	}

	newPath := exe + ".new"
	out, err := os.Create(newPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return err
	}
	out.Close()

	oldPath := exe + ".old"
	os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil {
		return fmt.Errorf("не удалось отодвинуть текущий exe: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		os.Rename(oldPath, exe) // откат
		return fmt.Errorf("не удалось поставить новый exe: %w", err)
	}
	return nil
}

// restartAfterUpdate запускает новый exe с небольшой задержкой (чтобы текущий процесс успел
// выйти и освободить порт панели 8737 и сетевой адаптер — иначе новый экземпляр по single-
// instance просто покажет закрывающееся окно), затем гасит текущий процесс.
func restartAfterUpdate() {
	exe, _ := os.Executable()
	exe, _ = filepath.Abs(exe)
	exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"Start-Sleep -Seconds 2; Start-Process -FilePath '"+exe+"'").Start()
	time.Sleep(400 * time.Millisecond) // дать ответу дойти до панели
	sess.Stop()                        // снять адаптер
	os.Exit(0)                         // выход освобождает порт 8737
}

// cleanupOldExe чистит .old/.new-хвосты прошлого обновления. Windows не даёт удалить .old,
// пока exe запущен, поэтому убираем на следующем старте.
func cleanupOldExe() {
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".old")
		os.Remove(exe + ".new")
	}
}
