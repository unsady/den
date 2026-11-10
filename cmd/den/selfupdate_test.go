package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDownloadReplacesAnOldDen(t *testing.T) {
	name := "den-" + runtime.GOOS + "-" + runtime.GOARCH
	release := []byte("new den")
	sums := sum(release) + "  " + name + "\n" + sum([]byte("other")) + "  den-plan9-386\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SHA256SUMS":
			w.Write([]byte(sums))
		case "/" + name:
			w.Write(release)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	exe := filepath.Join(t.TempDir(), "den")
	os.WriteFile(exe, []byte("old den"), 0o755)
	changed, err := download(context.Background(), srv.URL, exe)
	got, _ := os.ReadFile(exe)
	if err != nil || !changed || string(got) != "new den" {
		t.Fatalf("changed %v, %v, file %q", changed, err, got)
	}
	if info, _ := os.Stat(exe); info.Mode()&0o111 == 0 {
		t.Fatal("not executable")
	}

	changed, err = download(context.Background(), srv.URL, exe)
	if err != nil || changed {
		t.Fatalf("second run: changed %v, %v", changed, err)
	}
}

func TestDownloadRefusesABadChecksum(t *testing.T) {
	name := "den-" + runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/SHA256SUMS" {
			w.Write([]byte(sum([]byte("expected")) + "  " + name + "\n"))
			return
		}
		w.Write([]byte("tampered"))
	}))
	defer srv.Close()

	exe := filepath.Join(t.TempDir(), "den")
	os.WriteFile(exe, []byte("old den"), 0o755)
	if _, err := download(context.Background(), srv.URL, exe); err == nil {
		t.Fatal("want an error")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old den" {
		t.Fatalf("file %q", got)
	}
}
