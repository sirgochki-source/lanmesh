//go:build windows

package main

import "testing"

func TestParseVer(t *testing.T) {
	cases := map[string][3]int{
		"v0.5.1":     {0, 5, 1},
		"0.5.1":      {0, 5, 1},
		"v1.0.0":     {1, 0, 0},
		"v0.5":       {0, 5, 0},
		"v2.10.3":    {2, 10, 3},
		"v0.5.1-rc1": {0, 5, 1},
	}
	for in, want := range cases {
		if got := parseVer(in); got != want {
			t.Errorf("parseVer(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	yes := [][2]string{
		{"v0.5.0", "v0.5.1"}, {"v0.5.1", "v0.6.0"}, {"v0.5.1", "v1.0.0"}, {"v0.9.9", "v0.10.0"},
	}
	no := [][2]string{
		{"v0.5.1", "v0.5.1"}, {"v0.5.1", "v0.5.0"}, {"v1.0.0", "v0.9.9"}, {"v0.10.0", "v0.9.9"},
	}
	for _, c := range yes {
		if !isNewer(c[0], c[1]) {
			t.Errorf("isNewer(%q,%q) = false, want true", c[0], c[1])
		}
	}
	for _, c := range no {
		if isNewer(c[0], c[1]) {
			t.Errorf("isNewer(%q,%q) = true, want false", c[0], c[1])
		}
	}
}
