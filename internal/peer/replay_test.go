package peer

import "testing"

// TestAcceptCounter проверяет скользящее окно anti-replay: свежие счётчики
// принимаются, повторы и заметно устаревшие — отбрасываются.
func TestAcceptCounter(t *testing.T) {
	ps := &peerState{}

	if !ps.acceptCounter(1000) {
		t.Fatal("первый счётчик должен приниматься")
	}
	if ps.acceptCounter(1000) {
		t.Fatal("повтор того же счётчика должен отклоняться")
	}
	if !ps.acceptCounter(1001) {
		t.Fatal("следующий счётчик должен приниматься")
	}

	// Пропущенный ранее, но в пределах окна — принимаем один раз.
	if !ps.acceptCounter(995) {
		t.Fatal("счётчик в окне должен приниматься")
	}
	if ps.acceptCounter(995) {
		t.Fatal("повтор счётчика из окна должен отклоняться")
	}

	// Ровно на границе окна (offset == 64) — уже слишком старый.
	if ps.acceptCounter(1001 - 64) {
		t.Fatal("счётчик за окном (>=64) должен отклоняться")
	}

	// Большой скачок вперёд сдвигает окно — прежний recvMax оказывается за ним.
	if !ps.acceptCounter(5000) {
		t.Fatal("скачок вперёд должен приниматься")
	}
	if ps.acceptCounter(1001) {
		t.Fatal("после сдвига окна старый счётчик должен отклоняться")
	}
}
