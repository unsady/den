package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The chat's agent runs fx acp, which fakeFX hands to this test binary:
// fakeACP answers as fakeFX does for fx ask, over the protocol.

func TestMain(m *testing.M) {
	if os.Getenv("DEN_FAKE_ACP") == "1" {
		fakeACP(os.Args[1:])
		return
	}
	os.Setenv("DEN_TEST_BINARY", os.Args[0])
	os.Exit(m.Run())
}

// fakeACP serves one fx acp process on stdin and stdout. A prompt's reply
// names the session it continued, "none" for a new one, the first line of
// the prompt, its count of lines, its images, the flags fx started with, and a
// message steered into the turn as steered=<text>.
func fakeACP(args []string) {
	flags := ""
	for i, arg := range args {
		switch arg {
		case "--provider", "--model", "--effort":
			flags += fmt.Sprintf(" %s=%s", strings.TrimPrefix(arg, "--"), args[i+1])
		}
	}

	var (
		wmu     sync.Mutex
		mu      sync.Mutex
		resume  = "none"
		session string
		steers  []json.RawMessage
		steered []string
		cancel  = make(chan struct{}, 1)
		running bool
	)
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		wmu.Lock()
		defer wmu.Unlock()
		data, _ := json.Marshal(v)
		out.Write(append(data, '\n'))
		out.Flush()
	}
	answer := func(id json.RawMessage, result any) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	update := func(u map[string]any) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session, "update": u}})
	}
	tool := func(title, key, value string) {
		update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": value, "title": title, "kind": "other", "status": "pending", "rawInput": map[string]any{key: value}})
	}

	prompt := func(id json.RawMessage, text string, images int) {
		os.WriteFile("last-prompt", []byte(text), 0o644)
		first, _, _ := strings.Cut(text, "\n")
		lines := 0
		for line := range strings.SplitSeq(text, "\n") {
			if strings.TrimSpace(line) != "" {
				lines++
			}
		}

		wait := func(d time.Duration) bool {
			select {
			case <-cancel:
				return false
			case <-time.After(d):
				return true
			}
		}
		reply, stop := "", "end_turn"
		switch {
		case strings.HasPrefix(first, "slow"):
			if !wait(time.Second) {
				stop = "cancelled"
			}
		case strings.HasPrefix(first, "steps"):
			tool("Reading", "path", "a.txt")
			tool("Running", "command", "ls")
			tool("Running", "command", "ls -la")
			if !wait(time.Second) {
				stop = "cancelled"
			}
		case strings.HasPrefix(first, "[den: quiet") || first == "ok":
			reply = "SILENT"
		case strings.HasPrefix(first, "quota") && !strings.Contains(flags, "effort=low"):
			reply, stop = "API request failed · HTTP 402 · out of credits", "refused"
		}

		mu.Lock()
		if reply == "" {
			reply = fmt.Sprintf("resume=%s msg=%s lines=%d%s", resume, first, lines, flags)
			if images > 0 {
				reply += fmt.Sprintf(" images=%d", images)
			}
			for _, s := range steered {
				reply += " steered=" + s
			}
		}
		resume = session
		held := steers
		steers, steered, running = nil, nil, false
		mu.Unlock()

		update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": reply}})
		result := map[string]any{"stopReason": stop}
		// A request the provider refused used no tokens.
		if stop != "refused" {
			result["usage"] = map[string]any{"inputTokens": 100, "outputTokens": 7}
		}
		answer(id, result)
		for _, s := range held {
			answer(s, map[string]any{"stopReason": stop, "_meta": map[string]any{"fx": map[string]any{"steering": "absorbed"}}})
		}
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 1<<20)
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"prompt"`
				Meta struct {
					Fx struct {
						Steer bool `json:"steer"`
					} `json:"fx"`
				} `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}

		switch m.Method {
		case "initialize":
			answer(m.ID, map[string]any{"protocolVersion": 1})
		case "session/new":
			session = "s1"
			answer(m.ID, map[string]any{"sessionId": session})
		case "session/resume":
			session, resume = m.Params.SessionID, m.Params.SessionID
			answer(m.ID, map[string]any{})
		case "session/cancel":
			select {
			case cancel <- struct{}{}:
			default:
			}
		case "session/prompt":
			var text []string
			images := 0
			for _, block := range m.Params.Prompt {
				if block.Type == "image" {
					images++
				} else {
					text = append(text, block.Text)
				}
			}
			mu.Lock()
			if m.Params.Meta.Fx.Steer && running {
				steers = append(steers, m.ID)
				steered = append(steered, strings.Join(text, "\n"))
				mu.Unlock()
				continue
			}
			running = true
			mu.Unlock()
			select {
			case <-cancel:
			default:
			}
			go prompt(m.ID, strings.Join(text, "\n"), images)
		}
	}
}

// TestSteer passes a message that comes during a turn into that turn, so it
// gets no turn of its own.
func TestSteer(t *testing.T) {
	cfg, telegram := startChat(t, nil)

	telegram.message(42, "slow")
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
		return string(data) == "slow"
	})
	id := telegram.update(42, map[string]any{"text": "and this"})
	telegram.expect(t, "resume=none msg=slow lines=1 steered=and this")

	waitFor(t, func() bool {
		telegram.mu.Lock()
		defer telegram.mu.Unlock()
		return strings.Contains(strings.Join(telegram.reacted, ","), fmt.Sprintf("%d ✍", id))
	})
	select {
	case got := <-telegram.sent:
		t.Fatalf("the steered message got a turn of its own: %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}

// waitFor waits until ok, for a few seconds.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 100 {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out")
}
