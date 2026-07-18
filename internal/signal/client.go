// Package signal — клиент сигнального сервера (Cloudflare Worker) и STUN.
package signal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// Client общается с воркером-сигналкой по HTTP.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient создаёт клиента для URL воркера (например
// https://your-worker.example.workers.dev).
//
// Keep-alive ВЫКЛЮЧЕН намеренно. Ходим сюда редко (регистрация раз в 20с, логи
// раз в 30с), и между запросами соединение простаивает. По дороге его тихо, без
// RST, убивает какой-нибудь посредник — NAT провайдера, DPI-обходчик или
// локальный VPN-клиент. Пул об этом не знает, отдаёт мёртвый сокет, запрос уходит
// в никуда и висит до таймаута — регистрация падает раз за разом, а узел молча
// пропадает из сети. Проверено: подряд идущие запросы с пулом начинают виснуть
// после пары простоев по 20с, а без пула в ту же секунду проходят за ~300мс.
// Экономия на переиспользовании тут — сотни миллисекунд раз в 20 секунд, то есть
// ничто по сравнению с ценой.
func NewClient(baseURL string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	// Прокси из окружения ОТКЛЮЧАЕМ. На машинах с корпоративным HTTP(S)_PROXY
	// Go по умолчанию гонит запросы через него, а прокси рубит CONNECT на
	// нестандартные порты (напр. сигналка на :25557 -> 403 Forbidden) — сигналка
	// ложно «недоступна». Весь боевой трафик lanmesh (STUN/relay/пробитие) и так
	// идёт напрямую по UDP мимо прокси; если прямого выхода в интернет нет, mesh
	// не заработает в принципе — так что ходить в сигналку прямо всегда верно.
	tr.Proxy = nil
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second, Transport: tr},
	}
}

// NetworkTag выводит несекретный тег сети для сигналки: hash сетевого ключа.
// Сервер сводит пиров по совпадению тега, но не может восстановить имя/пароль.
func NetworkTag(key [crypto.KeySize]byte) string {
	h := sha256.Sum256(append([]byte("lanmesh-tag|"), key[:]...))
	return hex.EncodeToString(h[:])
}

// Register объявляет пира в сети и забирает список остальных участников.
func (c *Client) Register(ctx context.Context, req proto.RegisterRequest) (*proto.RegisterResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/register", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Дочитываем тело (как в SendLogs) и подмешиваем в ошибку — сервер часто
		// пишет туда причину отказа (переполнен реестр, кривой тег и т.п.).
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("signal: register вернул %d: %s", resp.StatusCode, msg)
	}
	var out proto.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("signal: разбор ответа register: %w", err)
	}
	return &out, nil
}

// SendLogs отправляет пачку строк лога на сигналку. Ответ не разбираем: это
// диагностика, и падать из-за неё сеанс не должен.
func (c *Client) SendLogs(ctx context.Context, req proto.LogRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/log", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // дочитываем, чтобы соединение вернулось в пул
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("signal: log вернул %d", resp.StatusCode)
	}
	return nil
}
