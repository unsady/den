package main

import "testing"

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"1G": 1 << 30, "2g": 2 << 30, "1024M": 1 << 30, "512m": 512 << 20, "1.5G": 3 << 29, "2GB": 2 << 30, "4096": 4096,
	} {
		if got, ok := parseSize(in); !ok || got != want {
			t.Errorf("parseSize(%q) = %d, %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "lots", "-1G", "G", "0"} {
		if _, ok := parseSize(in); ok {
			t.Errorf("parseSize(%q) was accepted", in)
		}
	}
}
