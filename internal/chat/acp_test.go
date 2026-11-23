package chat

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsady/den/internal/store"
)

// The chat's agent runs fx acp, which fakeFX hands to this test binary:
// fakeACP is fx acp for the tests, run by fakeFX.

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
//
// As fx does, it saves a session with its first prompt, logs its prompt's
// size, the number in the space's context-size file, when there is one, in the index of
// sessions/ beside the space, and answers a resume of one it has not saved
// with ACP's resource-not-found code.
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
		save    func(string)
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
		os.WriteFile("den-session", []byte(os.Getenv("DEN_SESSION")), 0o644)
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
		case strings.HasPrefix(first, "slow"), strings.HasPrefix(first, "[den: quiet"):
			if !wait(time.Second) {
				stop = "cancelled"
			}
		case strings.HasPrefix(first, "steps"):
			tool("Reading", "path", "a.txt")
			tool("Running", "command", "ls")
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Listing the folder."}})
			tool("Running", "command", "ls -la")
			if !wait(time.Second) {
				stop = "cancelled"
			}
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
		save(session)
		// As fx does, the session's log tells how big its prompt was.
		if size, err := os.ReadFile("context-size"); err == nil {
			os.MkdirAll(filepath.Join("sessions", session), 0o755)
			f, _ := os.OpenFile(filepath.Join("sessions", session, "log.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			fmt.Fprintf(f, `{"kind":"item","type":"model_call","ts":%d,"data":{"purpose":"turn","outcome":"completed","input_tokens":%s}}`+"\n", time.Now().UnixMilli(), size)
			f.Close()
		}
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

	index := filepath.Join("sessions", "index.jsonl")
	saved := func(id string) bool {
		data, _ := os.ReadFile(index)
		for line := range strings.Lines(string(data)) {
			var e struct{ ID string }
			if json.Unmarshal([]byte(line), &e) == nil && e.ID == id {
				return true
			}
		}
		return false
	}
	save = func(id string) {
		if saved(id) {
			return
		}
		os.MkdirAll("sessions", 0o755)
		f, _ := os.OpenFile(index, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		fmt.Fprintf(f, `{"op":"put","id":%q,"role":"root","updated":%d}`+"\n", id, time.Now().UnixMilli())
		f.Close()
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
						Steer     bool   `json:"steer"`
						SessionID string `json:"sessionId"`
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
			session, resume = cmp.Or(m.Params.Meta.Fx.SessionID, "s1"), "none"
			answer(m.ID, map[string]any{"sessionId": session})
		case "session/resume":
			if !saved(m.Params.SessionID) {
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32002, "message": "Session not found"}})
				continue
			}
			session, resume = m.Params.SessionID, m.Params.SessionID
			answer(m.ID, map[string]any{})
		case "libfx/compact":
			// As fx's fork does: in place, under the session's id.
			f, _ := os.OpenFile("compacted", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			fmt.Fprintln(f, m.Params.SessionID)
			f.Close()
			answer(m.ID, map[string]any{"compacted": true, "tokensBefore": 50_000, "tokensAfter": 12_000})
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
			// As fx does for a model that cannot see pictures.
			if images > 0 && strings.HasPrefix(text[0], "blind") {
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32602, "message": "Image prompts are unavailable for the selected model"}})
				continue
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

// TestSteerOwnTurn passes a message into a turn on an agent of its own, such
// as one in a job's session, as into the chat agent's.
func TestSteerOwnTurn(t *testing.T) {
	cfg, telegram, c := startChatWith(t, nil)
	seed(t, c, store.Session{ID: "job1", Kind: store.KindJob, Title: "news", Choice: store.Choice{Provider: "p2", Model: "m"}})
	c.Sent([]int64{900}, "job1")
	reply := map[string]any{"message_id": 900}

	telegram.update(42, map[string]any{"text": "slow", "reply_to_message": reply})
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
		return string(data) == "slow"
	})
	id := telegram.update(42, map[string]any{"text": "and this", "reply_to_message": reply})
	telegram.expect(t, "⏰ news\n\nresume=job1 msg=slow lines=1 provider=p2 model=m steered=and this")

	telegram.mu.Lock()
	reacted := strings.Join(telegram.reacted, ",")
	telegram.mu.Unlock()
	if !strings.Contains(reacted, fmt.Sprintf("%d ✍", id)) {
		t.Fatalf("reacted %q", reacted)
	}
	select {
	case got := <-telegram.sent:
		t.Fatalf("the steered message got a turn of its own: %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestNoSteerAfterSwitch(t *testing.T) {
	cfg, telegram := startChat(t, nil)

	// A message after /new starts the new session rather than joining the
	// turn still running in the old one, and does not wait for it.
	telegram.message(42, "slow")
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(cfg.Space, "last-prompt"))
		return string(data) == "slow"
	})
	telegram.message(42, "/new")
	telegram.expect(t, "✦ New session · <b>auto</b> · p1")
	id := telegram.update(42, map[string]any{"text": "next"})
	telegram.expectAll(t, "resume=none msg=next lines=1", "↩ Earlier session\n\nresume=none msg=slow lines=1")

	telegram.mu.Lock()
	defer telegram.mu.Unlock()
	if slices.Contains(telegram.reacted, fmt.Sprintf("%d 👀", id)) {
		t.Fatal("the message for a free session was marked as waiting")
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
