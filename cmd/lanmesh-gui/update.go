//go:build windows

package main

import (
	"encoding/json"
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
const version = "v0.5.1"

const githubLatestAPI = "https://api.github.com/repos/sirgochki-source/lanmesh/releases/latest"

// ghRelease — нужные поля ответа GitHub releases/latest.
type ghRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// latestRelease тянет последний релиз с GitHub и возвращает его + URL ассета lanmesh-gui.exe.
func latestRelease() (*ghRelease, string, error) {
	req, _ := http.NewRequest("GET", githubLatestAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lanmesh-gui")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GitHub API: %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, "", err
	}
	var asset string
	for _, a := range rel.Assets {
		if a.Name == "lanmesh-gui.exe" {
			asset = a.URL
			break
		}
	}
	return &rel, asset, nil
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
	rel, asset, err := latestRelease()
	if err != nil {
		writeJSON(w, map[string]string{"error": "не удалось проверить: " + err.Error()}, http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"current":   version,
		"latest":    rel.TagName,
		"name":      rel.Name,
		"hasUpdate": asset != "" && isNewer(version, rel.TagName),
	}, http.StatusOK)
}

// handleUpdate: POST — скачивает новый lanmesh-gui.exe, заменяет текущий и перезапускается.
func handleUpdate(w http.ResponseWriter, r *http.Request) {
	rel, asset, err := latestRelease()
	if err != nil || asset == "" {
		writeJSON(w, map[string]string{"error": "нет доступного обновления"}, http.StatusBadGateway)
		return
	}
	if !isNewer(version, rel.TagName) {
		writeJSON(w, map[string]bool{"ok": true, "upToDate": true}, http.StatusOK)
		return
	}
	if err := selfUpdate(asset); err != nil {
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
func selfUpdate(assetURL string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.Abs(exe)

	req, _ := http.NewRequest("GET", assetURL, nil)
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
	// Detached PowerShell: ждёт ~2с и запускает новый exe (тот сам поднимет UAC при старте).
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
