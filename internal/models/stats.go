package models

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The proxy measures each model request, so the user can ask how a
// provider did: how long it stayed silent before the first token, whether
// the stream stalled, how fast it wrote, and what failed. fx's own logs
// have the totals but not these timings.

const (
	// callsFile keeps the recent calls in den's state, one JSON per line.
	callsFile = "model-calls.jsonl"
	// keptCalls is how many calls den keeps.
	keptCalls = 2000
)

// Call is one model request through the proxy.
type Call struct {
	At       time.Time `json:"at"`
	Provider string    `json:"provider"`
	Model    string    `json:"model,omitempty"`
	Status   int       `json:"status"`
	// First is how long the provider was silent before the first token,
	// Gap the longest silence after it, and Total the whole request, in
	// milliseconds. First is 0 when no token came.
	First int `json:"first_ms"`
	Gap   int `json:"gap_ms"`
	Total int `json:"total_ms"`
	In    int `json:"in_tokens,omitempty"`
	Out   int `json:"out_tokens,omitempty"`
	// Error is the start of a failed answer, or why den could not reach the
	// provider.
	Error string `json:"error,omitempty"`
}

// Calls returns the calls since a time, oldest first.
func (m *Models) Calls(since time.Time) []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Call
	for _, c := range m.calls {
		if !c.At.Before(since) {
			out = append(out, c)
		}
	}
	return out
}

func (m *Models) loadCalls() {
	f, err := os.Open(filepath.Join(m.cfg.State, callsFile))
	if err != nil {
		return
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(nil, 1<<20)
	for scan.Scan() {
		var c Call
		if json.Unmarshal(scan.Bytes(), &c) == nil {
			m.calls = append(m.calls, c)
		}
	}
	m.written = len(m.calls)
	if len(m.calls) > keptCalls {
		m.calls = m.calls[len(m.calls)-keptCalls:]
	}
}

// addCall keeps a call and appends it to the file, which it rewrites with
// the kept calls once it holds twice as many.
func (m *Models) addCall(c Call) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, c)
	if len(m.calls) > keptCalls {
		m.calls = m.calls[len(m.calls)-keptCalls:]
	}
	if m.cfg.State == "" {
		return
	}

	path := filepath.Join(m.cfg.State, callsFile)
	if m.written >= 2*keptCalls {
		var buf bytes.Buffer
		for _, k := range m.calls {
			line, _ := json.Marshal(k)
			buf.Write(append(line, '\n'))
		}
		if err := os.WriteFile(path+".tmp", buf.Bytes(), 0o600); err == nil {
			os.Rename(path+".tmp", path)
			m.written = len(m.calls)
		}
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("models: %v", err)
		return
	}
	defer f.Close()
	line, _ := json.Marshal(c)
	f.Write(append(line, '\n'))
	m.written++
}

var (
	// firstToken finds a chunk that carries text: an answer, reasoning, or
	// a tool call's arguments, in Chat Completions and the Responses API.
	firstToken = regexp.MustCompile(`"(?:content|reasoning_content|reasoning|text|delta|arguments)"\s*:\s*"[^"]`)
	// outputTokens finds the answer's size in either API's usage.
	outputTokens = regexp.MustCompile(`"(?:completion|output)_tokens"\s*:\s*(\d+)`)
)

// meter passes a response through and measures it as it streams.
type meter struct {
	io.ReadCloser
	m    *Models
	call Call
	// start is when den got the request.
	start time.Time
	first time.Time
	last  time.Time
	// tail keeps the end of the previous read, so a field split between
	// reads is still found.
	tail []byte
	// errText keeps the start of a failed answer.
	errText []byte
	done    bool
}

func (m *Models) measure(body io.ReadCloser, start time.Time, provider, model string, status int) *meter {
	return &meter{ReadCloser: body, m: m, start: start, call: Call{At: start, Provider: provider, Model: model, Status: status}}
}

func (r *meter) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		now := time.Now()
		window := append(r.tail, p[:n]...)
		if r.first.IsZero() {
			if firstToken.Match(window) {
				r.first, r.last = now, now
			}
		} else {
			r.call.Gap = max(r.call.Gap, int(now.Sub(r.last).Milliseconds()))
			r.last = now
		}
		// Codex also counts tokens per tool and per input item, each part of
		// its usage, so the largest count is the whole.
		for _, match := range promptTokens.FindAllSubmatch(window, -1) {
			r.call.In = max(r.call.In, atoi(match[1]))
		}
		for _, match := range outputTokens.FindAllSubmatch(window, -1) {
			r.call.Out = max(r.call.Out, atoi(match[1]))
		}
		if r.call.Status >= 400 && len(r.errText) < 300 {
			r.errText = append(r.errText, p[:min(n, 300-len(r.errText))]...)
		}
		r.tail = append([]byte(nil), window[max(0, len(window)-64):]...)
	}
	if err != nil {
		r.finish()
	}
	return n, err
}

func atoi(digits []byte) int {
	n, _ := strconv.Atoi(string(digits))
	return n
}

func (r *meter) Close() error {
	r.finish()
	return r.ReadCloser.Close()
}

func (r *meter) finish() {
	if r.done {
		return
	}
	r.done = true
	r.call.Total = int(time.Since(r.start).Milliseconds())
	if !r.first.IsZero() {
		r.call.First = int(r.first.Sub(r.start).Milliseconds())
	}
	if len(r.errText) > 0 {
		r.call.Error = strings.Join(strings.Fields(string(r.errText)), " ")
	}
	if r.call.In > 0 {
		r.m.record(r.call.In)
	}
	r.m.addCall(r.call)
}

// generation says whether a proxied path asks a model for an answer, as
// opposed to listing models.
func generation(path string) bool {
	return strings.HasSuffix(path, "chat/completions") || strings.HasSuffix(path, "responses")
}

// requestModel reads the model a request names, and leaves the body for
// the provider.
func requestModel(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req)
	return req.Model
}
