// Package logbuf — кольцевой буфер строк лога поверх обычного log.Output.
//
// Зачем: чтобы диагностику с чужой машины можно было забрать с сигналки, а не
// просить человека вручную присылать gui.log. Буфер вешается тройником рядом с
// файлом (log.SetOutput(io.MultiWriter(file, buf))), копит строки и отдаёт
// накопленное отправщику.
package logbuf

import (
	"strings"
	"sync"
)

// Buffer — потокобезопасный накопитель строк лога.
//
// Держит две вещи: pending (ещё не отправленное) и общий лимит, чтобы при
// оборванной отправке память не росла бесконечно — лишнее вытесняется с головы,
// как в кольце.
type Buffer struct {
	mu      sync.Mutex
	pending []string
	max     int
	partial string // хвост без завершающего \n — log обычно пишет строку целиком, но не обязан
}

// New создаёт буфер, хранящий не больше max неотправленных строк.
func New(max int) *Buffer {
	if max <= 0 {
		max = 200
	}
	return &Buffer{max: max}
}

// Write реализует io.Writer: режет поток на строки и копит их.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.partial += string(p)
	for {
		i := strings.IndexByte(b.partial, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(b.partial[:i], "\r")
		b.partial = b.partial[i+1:]
		if line != "" {
			b.pending = append(b.pending, line)
		}
	}
	// Переполнение: выкидываем самые старые — свежие строки для диагностики важнее.
	if n := len(b.pending) - b.max; n > 0 {
		b.pending = append(b.pending[:0], b.pending[n:]...)
	}
	return len(p), nil
}

// Drain забирает накопленные строки и очищает буфер. Возвращает nil, если пусто —
// вызывающий по этому признаку может не ходить в сеть вообще.
func (b *Buffer) Drain() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return nil
	}
	out := b.pending
	b.pending = nil
	return out
}

// PutBack возвращает строки в начало очереди, если отправить их не удалось.
// Лимит соблюдаем: при переполнении жертвуем самыми старыми.
func (b *Buffer) PutBack(lines []string) {
	if len(lines) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending = append(lines, b.pending...)
	if n := len(b.pending) - b.max; n > 0 {
		b.pending = append(b.pending[:0], b.pending[n:]...)
	}
}
