package logbuf

import (
	"fmt"
	"log"
	"reflect"
	"testing"
)

func TestWriteSplitsLines(t *testing.T) {
	b := New(10)
	fmt.Fprintf(b, "первая\nвторая\n")

	got := b.Drain()
	want := []string{"первая", "вторая"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Drain() = %q, хотели %q", got, want)
	}
	if got := b.Drain(); got != nil {
		t.Fatalf("повторный Drain() = %q, хотели nil", got)
	}
}

// Строка может прийти несколькими Write — отдавать её нужно только целиком.
func TestPartialLineHeldUntilNewline(t *testing.T) {
	b := New(10)
	b.Write([]byte("нача"))
	if got := b.Drain(); got != nil {
		t.Fatalf("незавершённая строка утекла: %q", got)
	}
	b.Write([]byte("ло\n"))
	if got, want := b.Drain(), []string{"начало"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Drain() = %q, хотели %q", got, want)
	}
}

// При переполнении жертвуем самыми старыми: свежее для диагностики важнее.
func TestOverflowDropsOldest(t *testing.T) {
	b := New(3)
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(b, "строка%d\n", i)
	}
	got, want := b.Drain(), []string{"строка3", "строка4", "строка5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Drain() = %q, хотели %q", got, want)
	}
}

func TestPutBackKeepsOrderAndLimit(t *testing.T) {
	b := New(3)
	fmt.Fprintf(b, "новая\n")
	b.PutBack([]string{"старая1", "старая2"})

	got, want := b.Drain(), []string{"старая1", "старая2", "новая"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Drain() = %q, хотели %q", got, want)
	}

	// Возврат сверх лимита не должен раздувать буфер.
	fmt.Fprintf(b, "свежая\n")
	b.PutBack([]string{"древняя1", "древняя2", "древняя3"})
	if n := len(b.Drain()); n > 3 {
		t.Fatalf("после PutBack в буфере %d строк, лимит 3", n)
	}
}

// Буфер живёт под log.SetOutput — префикс с датой не должен ничего ломать.
func TestWorksAsLogOutput(t *testing.T) {
	b := New(10)
	lg := log.New(b, "", log.LstdFlags)
	lg.Printf("сеть %q поднята", "myteam")

	got := b.Drain()
	if len(got) != 1 {
		t.Fatalf("Drain() = %q, хотели одну строку", got)
	}
	if !contains(got[0], `сеть "myteam" поднята`) {
		t.Fatalf("строка потеряла содержимое: %q", got[0])
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
