package notify

import (
	"context"
	"encoding/json"
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
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "description": "PHOTO_INVALID_DIMENSIONS"})
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
