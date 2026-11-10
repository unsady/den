package transcribe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsady/den/internal/config"
)

func TestText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("no file: %v", err)
			return
		}
		audio, _ := io.ReadAll(file)
		if header.Filename != "voice.ogg" || string(audio) != "ogg bytes" {
			t.Errorf("file %q holds %q", header.Filename, audio)
		}
		if r.FormValue("model") != "whisper-large-v3-turbo" || r.FormValue("response_format") != "json" {
			t.Errorf("form %v", r.Form)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization %q", got)
		}
		_, _ = io.WriteString(w, `{"text":"  hello, the deploy is ready  "}`)
	}))
	defer server.Close()

	cfg := config.Config{
		Secrets:         t.TempDir(),
		TranscribeURL:   server.URL,
		TranscribeModel: "whisper-large-v3-turbo",
	}
	if err := os.WriteFile(filepath.Join(cfg.Secrets, "transcribe_key"), []byte("sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	text, err := Text(context.Background(), cfg, "/inbox/voice.ogg", []byte("ogg bytes"))
	if err != nil || text != "hello, the deploy is ready" {
		t.Fatalf("Text = %q, %v", text, err)
	}
}

func TestTextErrors(t *testing.T) {
	if _, err := Text(context.Background(), config.Config{}, "/inbox/voice.ogg", nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unset URL: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := Text(context.Background(), config.Config{TranscribeURL: server.URL}, "/inbox/voice.ogg", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 500: model not loaded") {
		t.Fatalf("server error: %v", err)
	}
}

// fakeTools puts opusdec and whisper-cli stand-ins first on PATH.
// whisper-cli prints what opusdec "decoded" with its language and model.
func fakeTools(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	tools := map[string]string{
		"opusdec": "#!/bin/sh\ncp \"$4\" \"$5\"\n",
		"whisper-cli": `#!/bin/sh
while [ $# -gt 0 ]; do
	case "$1" in
	--language) lang=$2; shift ;;
	--model) model=$(cat "$2"); shift ;;
	--file) file=$2; shift ;;
	esac
	shift
done
printf ' heard %s\n lang=%s model=%s\n' "$(cat "$file")" "$lang" "$model"
`,
	}
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLocal(t *testing.T) {
	fakeTools(t)
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ggml-tiny-q8_0.bin" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "weights")
	}))
	defer models.Close()
	Models = models.URL

	dir := t.TempDir()
	cfg := config.Config{State: dir, WhisperModel: "tiny-q8_0", WhisperLanguage: "en"}
	voice := filepath.Join(dir, "voice.ogg")
	if err := os.WriteFile(voice, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Text(context.Background(), cfg, voice, nil); err == nil || !strings.Contains(err.Error(), "not downloaded yet") {
		t.Fatalf("before the download: %v", err)
	}
	if err := Prepare(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	text, err := Text(context.Background(), cfg, voice, nil)
	if err != nil || text != "heard hello lang=en model=weights" {
		t.Fatalf("Text = %q, %v", text, err)
	}

	for _, bad := range []string{"../secrets", "Large"} {
		cfg.WhisperModel = bad
		if err := Prepare(context.Background(), cfg); err == nil {
			t.Errorf("Prepare accepted the model name %q", bad)
		}
	}
	cfg.WhisperModel = "missing"
	if err := Prepare(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("missing model: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "models", "ggml-missing.bin.part")); !os.IsNotExist(err) {
		t.Fatalf("a failed download left its part file: %v", err)
	}
}
