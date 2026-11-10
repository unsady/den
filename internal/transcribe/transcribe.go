// Package transcribe turns voice messages into text, either locally with
// whisper.cpp or through a service with OpenAI's transcription API.
//
// Local transcription starts whisper-cli for each message, so the model
// takes memory only while it works. The model is downloaded once into den's
// state when DEN_WHISPER_MODEL names one.
package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/proc"
)

// ErrNotConfigured means neither DEN_TRANSCRIBE_URL nor DEN_WHISPER_MODEL is set.
var ErrNotConfigured = errors.New("no speech-to-text is set up")

// Models is where whisper.cpp models are downloaded from. Tests point it at
// a local server.
var Models = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main"

// timeout bounds one transcription, a minute of speech on a slow CPU included.
const timeout = 3 * time.Minute

var modelName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Text returns what the voice message at path says. audio is its contents.
// A service named by DEN_TRANSCRIBE_URL takes precedence over a local model.
func Text(ctx context.Context, cfg config.Config, path string, audio []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch {
	case cfg.TranscribeURL != "":
		return remote(ctx, cfg, filepath.Base(path), audio)
	case cfg.WhisperModel != "":
		return local(ctx, cfg, path)
	}
	return "", ErrNotConfigured
}

// ModelPath is where the local model lives once downloaded.
func ModelPath(cfg config.Config) string {
	return filepath.Join(cfg.State, "models", "ggml-"+cfg.WhisperModel+".bin")
}

// Prepare downloads the local model when it is set and missing.
func Prepare(ctx context.Context, cfg config.Config) error {
	if cfg.WhisperModel == "" || cfg.TranscribeURL != "" {
		return nil
	}
	if !modelName.MatchString(cfg.WhisperModel) {
		return fmt.Errorf("DEN_WHISPER_MODEL %q is not a whisper.cpp model name", cfg.WhisperModel)
	}
	path := ModelPath(cfg)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	log.Printf("transcribe: downloading the whisper model %s", cfg.WhisperModel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Models+"/ggml-"+cfg.WhisperModel+".bin", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", cfg.WhisperModel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", cfg.WhisperModel, resp.StatusCode)
	}

	part := path + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(part, path)
	}
	if err != nil {
		os.Remove(part)
		return fmt.Errorf("downloading %s: %w", cfg.WhisperModel, err)
	}
	log.Printf("transcribe: downloaded %s (%d MB)", cfg.WhisperModel, n>>20)
	return nil
}

// script converts Telegram's Ogg Opus to the WAV whisper-cli reads and
// prints the transcript without timestamps.
const script = `set -eu
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
opusdec --quiet --rate 16000 "$1" "$tmp/voice.wav"
whisper-cli --no-prints --no-timestamps --language "$2" --threads "$3" --model "$4" --file "$tmp/voice.wav"`

// local runs whisper-cli as the agent, which owns the saved message.
func local(ctx context.Context, cfg config.Config, path string) (string, error) {
	model := ModelPath(cfg)
	if _, err := os.Stat(model); err != nil {
		return "", fmt.Errorf("the whisper model %s is not downloaded yet; den's log shows the download", cfg.WhisperModel)
	}

	cmd, err := proc.Command(ctx, cfg, "/bin/sh", "-c", script, "transcribe",
		path, cfg.WhisperLanguage, strconv.Itoa(runtime.NumCPU()), model)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	ooms := proc.OOMKills()
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("whisper did not finish in %s", timeout)
		}
		if proc.OutOfMemory(err, ooms) {
			return "", fmt.Errorf("whisper ran out of memory: the container has %s; a smaller DEN_WHISPER_MODEL needs less", proc.MemoryLimit())
		}
		return "", fmt.Errorf("whisper failed: %v: %s", err, clip(stderr.String()))
	}
	return strings.Join(strings.Fields(stdout.String()), " "), nil
}

// remote sends the audio, a file called name, to the service. A key in the
// secrets file transcribe_key is sent as a bearer token.
func remote(ctx context.Context, cfg config.Config, name string, audio []byte) (string, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("file", name)
	_, _ = part.Write(audio)
	if cfg.TranscribeModel != "" {
		_ = form.WriteField("model", cfg.TranscribeModel)
	}
	if cfg.WhisperLanguage != "" && cfg.WhisperLanguage != "auto" {
		_ = form.WriteField("language", cfg.WhisperLanguage)
	}
	_ = form.WriteField("response_format", "json")
	_ = form.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TranscribeURL, &body)
	if err != nil {
		return "", fmt.Errorf("bad DEN_TRANSCRIBE_URL: %w", err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	if key := secret(cfg); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("the service did not answer in %s", timeout)
		}
		return "", fmt.Errorf("the service is unreachable: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the service answered HTTP %d: %s", resp.StatusCode, clip(string(data)))
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("the service answered without a transcript: %s", clip(string(data)))
	}
	return strings.TrimSpace(result.Text), nil
}

func secret(cfg config.Config) string {
	data, err := os.ReadFile(filepath.Join(cfg.Secrets, "transcribe_key"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func clip(text string) string {
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > 200 {
		return string(runes[len(runes)-200:])
	}
	return text
}
