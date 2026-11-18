package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSendFile(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		field := map[string]string{"sendPhoto": "photo", "sendDocument": "document"}[method]
		file, header, err := r.FormFile(field)
		if err != nil {
			t.Errorf("%s without %s: %v", method, field, err)
			return
		}
		data, _ := io.ReadAll(file)
		mu.Lock()
		calls = append(calls, method+" "+header.Filename+" "+string(data)+" "+r.FormValue("caption"))
		mu.Unlock()

		// Telegram refuses a photo it cannot show, such as a very tall one.
		ok := method == "sendDocument" || !strings.Contains(header.Filename, "tall")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "description": "PHOTO_INVALID_DIMENSIONS", "result": map[string]int{"message_id": 1}})
	}))
	defer server.Close()
	API = server.URL
	telegram := &Telegram{token: "token", chatID: "42", client: server.Client()}

	for _, f := range []struct{ name, caption string }{
		{"chart.PNG", "Sales"},
		{"report.pdf", ""},
		{"tall.jpg", ""},
	} {
		if err := telegram.SendFile(context.Background(), f.name, []byte("bytes"), f.caption); err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
	}

	want := []string{
		"sendPhoto chart.PNG bytes Sales",
		"sendDocument report.pdf bytes ",
		"sendPhoto tall.jpg bytes ",
		"sendDocument tall.jpg bytes ",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	if err := telegram.SendFile(context.Background(), "big.bin", make([]byte, MaxUpload+1), ""); err == nil {
		t.Fatal("sent a file over the upload limit")
	}
}

func TestSendFiles(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("%s: %v", method, err)
			return
		}
		call := method
		var media []map[string]string
		_ = json.Unmarshal([]byte(r.FormValue("media")), &media)
		for _, m := range media {
			name := strings.TrimPrefix(m["media"], "attach://")
			call += " " + m["type"] + ":" + r.MultipartForm.File[name][0].Filename + ":" + m["caption"]
		}
		for _, field := range []string{"photo", "document"} {
			if f := r.MultipartForm.File[field]; len(f) > 0 {
				call += " " + field + ":" + f[0].Filename + ":" + r.FormValue("caption")
			}
		}
		calls = append(calls, call)

		var result any = map[string]int{"message_id": len(calls)}
		if method == "sendMediaGroup" {
			result = []map[string]int{{"message_id": 10 * len(calls)}, {"message_id": 10*len(calls) + 1}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	defer server.Close()
	API = server.URL
	telegram := &Telegram{token: "token", chatID: "42", client: server.Client()}

	ids, err := telegram.SendFiles(context.Background(), []Upload{
		{Name: "a.png", Data: []byte("a"), Caption: "Sales"},
		{Name: "report.pdf", Data: []byte("r")},
		{Name: "b.jpg", Data: []byte("b")},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"sendMediaGroup photo:a.png:Sales photo:b.jpg:",
		"sendDocument document:report.pdf:",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	if fmt.Sprint(ids) != "[10 11 2]" {
		t.Fatalf("ids %v", ids)
	}
}

func TestAlbums(t *testing.T) {
	files := make([]Upload, 12)
	if got := albums(files); len(got) != 2 || len(got[0]) != maxAlbum || len(got[1]) != 2 {
		t.Fatalf("12 files: %d albums", len(got))
	}
	big := []Upload{{Data: make([]byte, MaxUpload)}, {Data: []byte("x")}}
	if got := albums(big); len(got) != 2 {
		t.Fatalf("over the upload limit: %d albums", len(got))
	}
}
