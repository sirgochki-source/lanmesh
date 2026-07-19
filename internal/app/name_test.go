package app

import (
	"os"
	"testing"
)

func TestNodeName(t *testing.T) {
	host, _ := os.Hostname()
	s := &Session{}
	if got := s.nodeName(); got != host {
		t.Errorf("пустое имя должно давать hostname %q, получили %q", host, got)
	}
	s.SetName("  Игровой-ПК  ") // пробелы обрезаются
	if got := s.nodeName(); got != "Игровой-ПК" {
		t.Errorf("ожидали 'Игровой-ПК', получили %q", got)
	}
	s.SetName("") // сброс — снова hostname
	if got := s.nodeName(); got != host {
		t.Errorf("сброс имени должен вернуть hostname %q, получили %q", host, got)
	}
}
