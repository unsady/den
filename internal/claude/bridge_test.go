package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/fxsettings"
)

// TestMain runs the test binary as claude when a test asks for it.
func TestMain(m *testing.M) {
	if scenario := os.Getenv("DEN_FAKE_CLAUDE"); scenario != "" {
		fake(scenario)
		return
	}
	os.Exit(m.Run())
}

// fake plays claude in stream-json mode: it asks den's MCP endpoint for
// the tools when it starts and calls them as the scenario says, the way
// the recorded sessions do. What it gets is written to DEN_FAKE_LOG.
func fake(scenario string) {
	logFile, _ := os.OpenFile(os.Getenv("DEN_FAKE_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	note := func(format string, args ...any) { fmt.Fprintf(logFile, format+"\n", args...) }
	note("start")

	var url, auth, sid string
	sid = randomHex(4)
	note("args %s", strings.Join(os.Args[1:], " "))
	for i, arg := range os.Args {
		if arg == "--resume" {
			sid = "resumed-" + os.Args[i+1]
			note("resume %s", os.Args[i+1])
		}
		if arg == "--system-prompt-file" {
			data, _ := os.ReadFile(os.Args[i+1])
			note("system %s", data)
		}
		if strings.Contains(arg, "Bearer") {
			note("secret in args")
		}
		if arg == "--mcp-config" {
			data, err := os.ReadFile(os.Args[i+1])
			if err != nil {
				note("mcp config: %v", err)
			}
			var cfg struct {
				McpServers struct {
					Den struct {
						URL     string            `json:"url"`
						Headers map[string]string `json:"headers"`
					} `json:"den"`
				} `json:"mcpServers"`
			}
			json.Unmarshal(data, &cfg)
			url, auth = cfg.McpServers.Den.URL, cfg.McpServers.Den.Headers["Authorization"]
		}
	}

	rpc := func(id int, method string, params any) json.RawMessage {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
		req.Header.Set("Authorization", auth)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			note("mcp error %v", err)
			return nil
		}
		defer resp.Body.Close()
		var out struct {
			Result json.RawMessage `json:"result"`
			Error  *struct{ Message string }
		}
		json.NewDecoder(resp.Body).Decode(&out)
		if out.Error != nil {
			note("mcp error %s", out.Error.Message)
		}
		return out.Result
	}
	call := func(id int, toolUseID, name, args string) string {
		var arguments any
		json.Unmarshal([]byte(args), &arguments)
		result := rpc(id, "tools/call", map[string]any{"name": name, "arguments": arguments, "_meta": map[string]any{"claudecode/toolUseId": toolUseID}})
		var out struct {
			Content []struct{ Type, Text, MimeType string }
		}
		json.Unmarshal(result, &out)
		if len(out.Content) == 0 {
			return ""
		}
		note("mcp %s %s", toolUseID, out.Content[0].Text)
		for _, c := range out.Content[1:] {
			note("mcp %s %s %s", toolUseID, c.Type, c.MimeType)
		}
		return out.Content[0].Text
	}

	// The probe fails, as it does for den, and claude goes on.
	rpc(0, "server/discover", map[string]any{})
	rpc(1, "initialize", map[string]any{"protocolVersion": "2025-11-25"})
	var tools struct{ Tools []struct{ Name string } }
	json.Unmarshal(rpc(2, "tools/list", nil), &tools)
	var names []string
	for _, t := range tools.Tools {
		names = append(names, t.Name)
	}
	note("tools %s", strings.Join(names, ","))

	var mu sync.Mutex
	out := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		line, _ := json.Marshal(v)
		fmt.Printf("%s\n", line)
	}
	out(map[string]any{"type": "system", "subtype": "init", "session_id": sid})
	note("init %s", sid)
	stream := func(e map[string]any) { out(map[string]any{"type": "stream_event", "event": e}) }
	start := func() {
		stream(map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]int{"input_tokens": 2, "cache_creation_input_tokens": 10, "cache_read_input_tokens": 5, "output_tokens": 1}}})
	}
	block := func(i int, start map[string]any, deltas ...map[string]any) {
		stream(map[string]any{"type": "content_block_start", "index": i, "content_block": start})
		for _, d := range deltas {
			stream(map[string]any{"type": "content_block_delta", "index": i, "delta": d})
		}
		stream(map[string]any{"type": "content_block_stop", "index": i})
	}
	text := func(i int, parts ...string) {
		var deltas []map[string]any
		for _, p := range parts {
			deltas = append(deltas, map[string]any{"type": "text_delta", "text": p})
		}
		block(i, map[string]any{"type": "text", "text": ""}, deltas...)
	}
	use := func(i int, id string, parts ...string) {
		deltas := []map[string]any{{"type": "input_json_delta", "partial_json": ""}}
		for _, p := range parts {
			deltas = append(deltas, map[string]any{"type": "input_json_delta", "partial_json": p})
		}
		block(i, map[string]any{"type": "tool_use", "id": id, "name": "mcp__den__lookup", "input": map[string]any{}}, deltas...)
	}
	end := func(reason string) {
		stream(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}, "usage": map[string]int{"input_tokens": 2, "cache_creation_input_tokens": 10, "cache_read_input_tokens": 5, "output_tokens": 7}})
		stream(map[string]any{"type": "message_stop"})
	}
	result := func(isError bool, text string) {
		out(map[string]any{"type": "result", "subtype": "success", "is_error": isError, "result": text,
			"modelUsage": map[string]any{"claude-sonnet-test": map[string]int{"contextWindow": 1000000, "outputTokens": 7}}})
	}

	turns := make(chan struct{}, 8)
	go func() {
		in := bufio.NewReader(os.Stdin)
		for {
			line, err := in.ReadBytes('\n')
			if err != nil {
				os.Exit(0)
			}
			var msg struct{ Type string }
			json.Unmarshal(line, &msg)
			switch msg.Type {
			case "user":
				note("in %s", bytes.TrimSpace(line))
				turns <- struct{}{}
			case "control_request":
				note("interrupt")
				out(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success"}})
			}
		}
	}()

	for range turns {
		switch scenario {
		case "text":
			start()
			text(0, "Hel", "lo")
			end("end_turn")
			result(false, "Hello")
		case "error":
			result(true, "Not logged in · Please run /login")
		case "tool":
			start()
			use(0, "toolu_a", `{"key": `, `"alpha"}`)
			end("tool_use")
			got := call(3, "toolu_a", "lookup", `{"key":"alpha"}`)
			start()
			text(0, "Got: "+got)
			end("end_turn")
			result(false, "done")
		case "parallel":
			// Claude calls the tools one at a time, after the whole message.
			start()
			block(0, map[string]any{"type": "thinking", "thinking": ""}, map[string]any{"type": "thinking_delta", "thinking": "hmm"})
			use(1, "toolu_a", `{"key":"a"}`)
			use(2, "toolu_b", `{"key":"b"}`)
			end("tool_use")
			a := call(3, "toolu_a", "lookup", `{"key":"a"}`)
			b := call(4, "toolu_b", "lookup", `{"key":"b"}`)
			start()
			text(0, "A="+a+" B="+b)
			end("end_turn")
			result(false, "done")
		}
	}
}

// harness is den's side of the bridge behind a local server.
type harness struct {
	t   *testing.T
	c   *Claude
	srv *httptest.Server
	log string

	mu sync.Mutex
}

func setup(t *testing.T, scenario string) *harness {
	t.Helper()
	c := withState(t)
	if err := c.Save("sk-ant-oat01-x"); err != nil {
		t.Fatal(err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, c: c, log: filepath.Join(t.TempDir(), "fake.log")}
	c.bin = filepath.Join(t.TempDir(), "claude")
	script := fmt.Sprintf("#!/bin/sh\nDEN_FAKE_CLAUDE='%s' DEN_FAKE_LOG='%s' exec '%s' \"$@\"\n", scenario, h.log, exe)
	if err := os.WriteFile(c.bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/claude/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.Chat(w, r, body, "conv")
	})
	mux.HandleFunc("/claude-mcp/", func(w http.ResponseWriter, r *http.Request) {
		c.MCP(w, r, strings.TrimPrefix(r.URL.Path, "/claude-mcp/"))
	})
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	// Closing the server waits for the MCP calls that wait for fx.
	t.Cleanup(func() {
		c.bridge.mu.Lock()
		defer c.bridge.mu.Unlock()
		for _, s := range c.bridge.sessions {
			s.stop()
		}
	})
	c.base = h.srv.URL
	return h
}

// ask sends a Chat Completions request and returns the status and body.
func (h *harness) ask(stream bool, messages ...map[string]any) (int, string) {
	h.t.Helper()
	return h.askAt("", stream, messages...)
}

// askAt asks at an effort level.
func (h *harness) askAt(effort string, stream bool, messages ...map[string]any) (int, string) {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model": "sonnet", "stream": stream, "messages": messages, "reasoning_effort": effort,
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "lookup", "description": "Look a key up.",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"key": map[string]string{"type": "string"}}},
		}}},
	})
	resp, err := http.Post(h.srv.URL+"/claude/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// waitLog waits until the fake has written a line with want.
func (h *harness) waitLog(want string) string {
	h.t.Helper()
	for range 100 {
		data, _ := os.ReadFile(h.log)
		if strings.Contains(string(data), want) {
			return string(data)
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(h.log)
	h.t.Fatalf("the fake never logged %q:\n%s", want, data)
	return ""
}

// answer is a streamed reply, put back together.
type answer struct {
	text, finish string
	calls        []toolCall
	prompt, out  int
}

func parse(t *testing.T, body string) answer {
	t.Helper()
	var a answer
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("the stream does not end with [DONE]: %q", body)
	}
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				}
				FinishReason string `json:"finish_reason"`
			}
			Usage struct {
				Prompt int `json:"prompt_tokens"`
				Out    int `json:"completion_tokens"`
			}
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("%q: %v", data, err)
		}
		for _, ch := range chunk.Choices {
			a.text += ch.Delta.Content
			for _, tc := range ch.Delta.ToolCalls {
				if tc.Index == len(a.calls) {
					a.calls = append(a.calls, toolCall{})
				}
				c := &a.calls[tc.Index]
				c.ID += tc.ID
				c.Function.Name += tc.Function.Name
				c.Function.Arguments += tc.Function.Arguments
			}
			if ch.FinishReason != "" {
				a.finish = ch.FinishReason
			}
		}
		if chunk.Usage.Prompt > 0 {
			a.prompt, a.out = chunk.Usage.Prompt, chunk.Usage.Out
		}
	}
	return a
}

func user(text string) map[string]any   { return map[string]any{"role": "user", "content": text} }
func system(text string) map[string]any { return map[string]any{"role": "system", "content": text} }
func result(id, text string) map[string]any {
	return map[string]any{"role": "tool", "tool_call_id": id, "content": text}
}

// asked is the assistant message fx sends back for the calls it got.
func asked(calls []toolCall) map[string]any {
	var list []any
	for _, c := range calls {
		list = append(list, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Function.Name, "arguments": c.Function.Arguments}})
	}
	return map[string]any{"role": "assistant", "content": nil, "tool_calls": list}
}

func TestText(t *testing.T) {
	h := setup(t, "text")

	status, body := h.ask(true, system("be brief"), user("hi"))
	if status != 200 {
		t.Fatalf("status %d: %s", status, body)
	}
	a := parse(t, body)
	if a.text != "Hello" || a.finish != "stop" || len(a.calls) != 0 {
		t.Errorf("answer = %+v", a)
	}
	if a.prompt != 17 || a.out != 7 {
		t.Errorf("usage = %d in, %d out, want 17 in, 7 out", a.prompt, a.out)
	}
	log := h.waitLog(`"content":"hi"`)
	if n := len(h.c.bridge.sessions); n != 1 {
		t.Errorf("%d sessions after the turn, want the idle one", n)
	}
	// Any user can read a process's arguments.
	if strings.Contains(log, "secret in args") || strings.Contains(log, "mcp config:") {
		t.Errorf("the MCP secret must reach claude in a file:\n%s", log)
	}
}

// A request without tools, such as fx naming a session, leaves no process
// to take a place.
func TestNoToolsCloses(t *testing.T) {
	h := setup(t, "text")

	body, _ := json.Marshal(map[string]any{"model": "haiku", "messages": []any{system("name it"), user("hi")}})
	resp, err := http.Post(h.srv.URL+"/claude/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if s, c := h.counts(); s != 0 || c != 0 {
		t.Errorf("%d live, %d closed, want none", s, c)
	}
}

func TestTextWhole(t *testing.T) {
	h := setup(t, "text")

	status, body := h.ask(false, user("hi"))
	var got struct {
		Choices []struct {
			Message      struct{ Content string }
			FinishReason string `json:"finish_reason"`
		}
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || status != 200 || len(got.Choices) != 1 {
		t.Fatalf("status %d: %s", status, body)
	}
	if got.Choices[0].Message.Content != "Hello" || got.Choices[0].FinishReason != "stop" {
		t.Errorf("reply = %s", body)
	}
}

func TestToolRoundTrip(t *testing.T) {
	h := setup(t, "tool")
	question := user("what is alpha?")

	_, body := h.ask(true, system("be brief"), question)
	first := parse(t, body)
	if first.finish != "tool_calls" || len(first.calls) != 1 {
		t.Fatalf("first answer = %+v", first)
	}
	if c := first.calls[0]; c.ID != "toolu_a" || c.Function.Name != "lookup" || c.Function.Arguments != `{"key": "alpha"}` {
		t.Errorf("call = %+v", c)
	}

	_, body = h.ask(true, system("be brief"), question, asked(first.calls), result("toolu_a", "PELICAN"))
	second := parse(t, body)
	if second.text != "Got: PELICAN" || second.finish != "stop" {
		t.Errorf("second answer = %+v", second)
	}

	log := h.waitLog("mcp toolu_a PELICAN")
	if n := strings.Count(log, "start\n"); n != 1 {
		t.Errorf("%d processes started, want 1", n)
	}
	if !strings.Contains(log, "tools lookup\n") {
		t.Errorf("claude did not get fx's tools:\n%s", log)
	}
	if strings.Contains(log, "conversation-checkpoint") {
		t.Errorf("a checkpoint was sent:\n%s", log)
	}
}

// picture is a user message with text and an image, as fx sends one.
func picture(text string) map[string]any {
	return map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": text},
		map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64,aGVsbG8"}},
	}}
}

func TestImageGoesToClaude(t *testing.T) {
	h := setup(t, "text")
	h.ask(false, picture("what is this?"))

	h.waitLog(`"content":[{"text":"what is this?","type":"text"},{"source":{"data":"aGVsbG8","media_type":"image/png","type":"base64"},"type":"image"}]`)
}

func TestToolImageGoesWithItsResult(t *testing.T) {
	h := setup(t, "tool")
	question := user("what is alpha?")
	_, body := h.ask(true, question)
	first := parse(t, body)

	// fx sends a tool's images in a user message after the results.
	_, body = h.ask(true, question, asked(first.calls), result("toolu_a", "PELICAN"), picture(`The tool "lookup" returned 1 image(s).`))
	if second := parse(t, body); second.text != "Got: PELICAN" {
		t.Errorf("second answer = %+v", second)
	}

	log := h.waitLog("mcp toolu_a image image/png")
	if n := strings.Count(log, "start\n"); n != 1 {
		t.Errorf("%d processes started, want 1", n)
	}
}

func TestParallelToolCalls(t *testing.T) {
	h := setup(t, "parallel")
	question := user("look up a and b")

	_, body := h.ask(true, question)
	first := parse(t, body)
	if first.finish != "tool_calls" || len(first.calls) != 2 || first.text != "" {
		t.Fatalf("first answer = %+v", first)
	}
	if first.calls[0].ID != "toolu_a" || first.calls[1].ID != "toolu_b" || first.calls[1].Function.Arguments != `{"key":"b"}` {
		t.Errorf("calls = %+v", first.calls)
	}

	// fx may answer in any order.
	_, body = h.ask(true, question, asked(first.calls), result("toolu_b", "rb"), result("toolu_a", "ra"))
	if second := parse(t, body); second.text != "A=ra B=rb" {
		t.Errorf("second answer = %+v", second)
	}
	h.waitLog("mcp toolu_b rb")
}

func TestMCPNeedsTheSecret(t *testing.T) {
	h := setup(t, "text")
	post := func(auth string) int {
		req, _ := http.NewRequest("POST", h.srv.URL+"/claude-mcp/nobody", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	for _, auth := range []string{"", "Bearer nope", "Bearer "} {
		if got := post(auth); got != http.StatusUnauthorized {
			t.Errorf("%q: status %d, want 401", auth, got)
		}
	}
	// With the secret the request gets as far as finding no such session.
	if got := post("Bearer " + h.c.secret()); got != http.StatusNotFound {
		t.Errorf("with the secret: status %d, want 404", got)
	}
}

func TestNotLoggedIn(t *testing.T) {
	h := setup(t, "error")

	for _, stream := range []bool{true, false} {
		status, body := h.ask(stream, user("hi"))
		if status != http.StatusBadGateway || !strings.Contains(body, "Not logged in") {
			t.Errorf("stream=%v: status %d: %s", stream, status, body)
		}
	}
}

func TestUnmatchedHistoryStartsOver(t *testing.T) {
	h := setup(t, "tool")

	_, body := h.ask(true, user("what is alpha?"))
	if first := parse(t, body); first.finish != "tool_calls" {
		t.Fatalf("first answer = %+v", first)
	}

	// The history is not the one claude has: fx edited or compacted it.
	_, body = h.ask(true, user("q1"), map[string]any{"role": "assistant", "content": "a1"}, user("q2"))
	if second := parse(t, body); second.finish != "tool_calls" {
		t.Fatalf("second answer = %+v", second)
	}

	log := h.waitLog("<conversation-checkpoint>")
	for _, want := range []string{`user: q1\nassistant: a1\n</conversation-checkpoint>\nq2`, "interrupt"} {
		if !strings.Contains(log, want) {
			// The old process logs the interrupt a moment later.
			log = h.waitLog(want)
		}
	}
	if n := strings.Count(log, "start\n"); n != 2 {
		t.Errorf("%d processes started, want 2", n)
	}
	h.c.bridge.mu.Lock()
	n := len(h.c.bridge.sessions)
	h.c.bridge.mu.Unlock()
	if n != 1 {
		t.Errorf("%d sessions, want only the new one", n)
	}
}

func TestOpening(t *testing.T) {
	calls := []toolCall{{ID: "c1"}}
	calls[0].Function.Name, calls[0].Function.Arguments = "lookup", `{"key":"a"}`
	call := message{Role: "assistant", ToolCalls: calls}
	cases := []struct {
		name string
		msgs []message
		want string
	}{
		{"first", []message{{Role: "user", Content: json.RawMessage(`"hi"`)}}, "hi"},
		{"ends in a result", []message{
			{Role: "user", Content: json.RawMessage(`"q"`)}, call,
			{Role: "tool", ToolCallID: "c1", Content: json.RawMessage(`"A"`)},
		}, "<conversation-checkpoint>\nuser: q\nassistant called tool lookup({\"key\":\"a\"}) -> result: A\n</conversation-checkpoint>\nContinue from the last tool result."},
	}
	for _, tc := range cases {
		if got := opening(tc.msgs).text; got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// counts is how many live processes the bridge has, and closed how many
// it can resume.
func (h *harness) counts() (sessions, closed int) {
	h.c.bridge.mu.Lock()
	defer h.c.bridge.mu.Unlock()
	return len(h.c.bridge.sessions), len(h.c.bridge.closed)
}

// say asks for a plain answer and checks it.
func (h *harness) say(messages ...map[string]any) {
	h.t.Helper()
	status, body := h.ask(true, messages...)
	if a := parse(h.t, body); status != 200 || a.text != "Hello" {
		h.t.Fatalf("status %d, answer %+v", status, a)
	}
}

func assistant(text string) map[string]any {
	return map[string]any{"role": "assistant", "content": text}
}

func TestSecondTurnContinuesTheProcess(t *testing.T) {
	h := setup(t, "text")

	h.say(system("be brief"), user("hi"))
	h.say(system("be brief"), user("hi"), assistant("Hello"), user("again"), user("and more"))

	log := h.waitLog(`and more`)
	if n := strings.Count(log, "start\n"); n != 1 {
		t.Errorf("%d processes started, want 1:\n%s", n, log)
	}
	if !strings.Contains(log, `"content":"again\n\nand more"`) || strings.Contains(log, "conversation-checkpoint") {
		t.Errorf("the new messages are not one plain turn:\n%s", log)
	}
}

func TestIdleProcessResumes(t *testing.T) {
	h := setup(t, "text")
	h.c.bridge.idle = 50 * time.Millisecond

	h.say(user("hi"))
	for range 100 {
		if s, c := h.counts(); s == 0 && c == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s, c := h.counts(); s != 0 || c != 1 {
		t.Fatalf("%d live, %d closed, want 0 and 1", s, c)
	}

	h.say(user("hi"), assistant("Hello"), user("again"))

	log := h.waitLog(`"content":"again"`)
	_, sid, _ := strings.Cut(log, "init ")
	sid, _, _ = strings.Cut(sid, "\n")
	if n := strings.Count(log, "start\n"); n != 2 || !strings.Contains(log, "resume "+sid+"\n") || !strings.Contains(log, "--resume "+sid) {
		t.Errorf("want a second process resumed from %q:\n%s", sid, log)
	}
	if strings.Contains(log, "conversation-checkpoint") {
		t.Errorf("a checkpoint was sent:\n%s", log)
	}
	if _, c := h.counts(); c != 0 {
		t.Errorf("%d closed sessions stayed after the resume", c)
	}
}

func TestSystemChangeResumes(t *testing.T) {
	h := setup(t, "text")

	h.say(system("first"), user("hi"))
	h.say(system("second"), user("hi"), assistant("Hello"), user("again"))

	log := h.waitLog(`"content":"again"`)
	if n := strings.Count(log, "start\n"); n != 2 || !strings.Contains(log, "--resume ") {
		t.Errorf("want a second process with --resume:\n%s", log)
	}
	if !strings.Contains(log, "system first\n") || !strings.Contains(log, "system second\n") {
		t.Errorf("the new process did not read the new system prompt:\n%s", log)
	}
	if s, c := h.counts(); s != 1 || c != 0 {
		t.Errorf("%d live, %d closed, want 1 and 0", s, c)
	}
}

func TestEffortChangeResumes(t *testing.T) {
	h := setup(t, "text")

	h.askAt("high", true, user("hi"))
	h.askAt("max", true, user("hi"), assistant("Hello"), user("again"))

	log := h.waitLog(`"content":"again"`)
	if !strings.Contains(log, "--effort high") || !strings.Contains(log, "--effort max --resume ") {
		t.Errorf("want --effort high, then max with --resume:\n%s", log)
	}
}

func TestWindowIsLearned(t *testing.T) {
	h := setup(t, "text")
	if w := Window(h.c.cfg, "sonnet"); w != window {
		t.Fatalf("window before claude says = %d", w)
	}

	h.say(user("hi"))

	for range 100 {
		if Window(h.c.cfg, "sonnet") == 1000000 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if w := Window(h.c.cfg, "sonnet"); w != 1000000 {
		t.Fatalf("window = %d, want claude's 1000000", w)
	}
	data, _ := os.ReadFile(fxsettings.Path(h.c.cfg))
	if !strings.Contains(string(data), `"sonnet":{"context_window":1000000`) {
		t.Errorf("fx was not told:\n%s", data)
	}
}

func TestLimitClosesTheOldest(t *testing.T) {
	h := setup(t, "text")
	h.c.bridge.max = 1

	h.say(user("one"))
	h.say(user("two"))

	if s, c := h.counts(); s != 1 || c != 1 {
		t.Errorf("%d live, %d closed, want 1 and 1", s, c)
	}
	if n := strings.Count(h.waitLog("start\n"), "start\n"); n != 2 {
		t.Errorf("%d processes started, want 2", n)
	}
}

func TestBusySessionIsNotTaken(t *testing.T) {
	h := setup(t, "text")
	h.say(user("hi"))

	h.c.bridge.mu.Lock()
	var s *session
	for _, s = range h.c.bridge.sessions {
	}
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	h.c.bridge.mu.Unlock()

	msgs := []message{{Role: "user", Content: json.RawMessage(`"hi"`)}, {Role: "assistant", Content: json.RawMessage(`"Hello"`)}, {Role: "user", Content: json.RawMessage(`"again"`)}}
	fps := []string{fingerprint(msgs[0]), fingerprint(msgs[1]), fingerprint(msgs[2])}
	got, _, why := h.c.resume(msgs, fps, s.key, "conv", s.system)
	if got != nil || why != "busy" {
		t.Errorf("took a busy session: %v, %q", got, why)
	}

	s.mu.Lock()
	s.free()
	s.mu.Unlock()
	if got, turn, why := h.c.resume(msgs, fps, s.key, "conv", s.system); got != s || turn.text != "again" {
		t.Errorf("did not take the idle session: %v, %q, %q", got, turn.text, why)
	}
}

func TestNoMemoryRefusesAProcessOverTheLimit(t *testing.T) {
	h := setup(t, "text")
	h.c.bridge.max = 1
	free := int64(minFree - 1)
	h.c.bridge.free = func() (int64, bool) { return free, true }

	h.say(user("one"))
	// Under the limit the memory is not checked.
	if s, _ := h.counts(); s != 1 {
		t.Fatalf("%d live, want 1", s)
	}

	// Hold the only process, so none can be closed.
	h.c.bridge.mu.Lock()
	for _, s := range h.c.bridge.sessions {
		s.mu.Lock()
		s.busy = true
		s.mu.Unlock()
	}
	h.c.bridge.mu.Unlock()

	body, _ := json.Marshal(map[string]any{"model": "sonnet", "messages": []any{map[string]any{"role": "user", "content": "other"}}})
	rec := httptest.NewRecorder()
	h.c.Chat(rec, httptest.NewRequest("POST", "/", bytes.NewReader(body)), body, "conv2")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "30" {
		t.Errorf("got %d, Retry-After %q, want 429 and 30", rec.Code, rec.Header().Get("Retry-After"))
	}
	if s, _ := h.counts(); s != 1 {
		t.Errorf("%d live after the refusal, want 1", s)
	}

	free = minFree
	rec = httptest.NewRecorder()
	h.c.Chat(rec, httptest.NewRequest("POST", "/", bytes.NewReader(body)), body, "conv2")
	if rec.Code != http.StatusOK {
		t.Errorf("with room, got %d: %s", rec.Code, rec.Body)
	}
	if n := strings.Count(h.waitLog("start\n"), "start\n"); n != 2 {
		t.Errorf("%d processes started, want 2", n)
	}
}

func TestIdleProcessClosesWithoutMemoryCheck(t *testing.T) {
	h := setup(t, "text")
	h.c.bridge.max = 1
	h.c.bridge.free = func() (int64, bool) { t.Error("memory checked although a process could close"); return 0, true }

	h.say(user("one"))
	h.say(user("two"))
}

func TestLimitFromConfig(t *testing.T) {
	if c := New(config.Config{ClaudeProcesses: 4}); c.bridge.limit() != 4 {
		t.Errorf("limit = %d, want 4", c.bridge.limit())
	}
	if c := New(config.Config{}); c.bridge.limit() != 2 {
		t.Errorf("limit = %d, want 2", c.bridge.limit())
	}
}

func TestInactiveFile(t *testing.T) {
	if n := inactiveFile("anon 10\nfile 50\ninactive_file 30\nactive_file 20\n"); n != 30 {
		t.Errorf("inactive_file = %d, want 30", n)
	}
	if n := inactiveFile("anon 10\n"); n != 0 {
		t.Errorf("without the line = %d, want 0", n)
	}
}
