package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is the server side of a Conn in a test.
type fake struct {
	t   *testing.T
	in  *bufio.Reader
	out io.WriteCloser
}

func newPair(t *testing.T) (*Conn, *fake) {
	t.Helper()

	toServer, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	c := New(clientIn, clientOut)
	t.Cleanup(func() {
		clientOut.Close()
		serverOut.Close()
	})
	return c, &fake{t: t, in: bufio.NewReader(toServer), out: serverOut}
}

// recv reads the next message of the client.
func (f *fake) recv() map[string]any {
	f.t.Helper()

	line, err := f.in.ReadBytes('\n')
	if err != nil {
		f.t.Errorf("fake: read: %v", err)
		return map[string]any{}
	}

	var m map[string]any
	err = json.Unmarshal(line, &m)
	if err != nil {
		f.t.Errorf("fake: bad line %q: %v", line, err)
	}
	return m
}

func (f *fake) send(v any) {
	f.t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		f.t.Errorf("fake: marshal: %v", err)
		return
	}
	_, err = f.out.Write(append(b, '\n'))
	if err != nil {
		f.t.Errorf("fake: write: %v", err)
	}
}

func (f *fake) reply(req map[string]any, result any) {
	f.t.Helper()
	f.send(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": result})
}

func (f *fake) fail(req map[string]any, code int, message string) {
	f.t.Helper()
	f.send(map[string]any{"jsonrpc": "2.0", "id": req["id"], "error": map[string]any{"code": code, "message": message}})
}

func (f *fake) update(session string, update map[string]any) {
	f.t.Helper()
	f.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session, "update": update}})
}

func (f *fake) text(session, text string) {
	f.t.Helper()
	f.update(session, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"messageId":     "m1",
		"content":       map[string]any{"type": "text", "text": text},
	})
}

// expect reads the next message and checks its method.
func (f *fake) expect(method string) map[string]any {
	f.t.Helper()

	m := f.recv()
	if m["method"] != method {
		f.t.Errorf("fake: got method %v, want %s", m["method"], method)
	}
	return m
}

func params(m map[string]any) map[string]any {
	p, _ := m["params"].(map[string]any)
	return p
}

// within fails the test when f does not return in time.
func within(t *testing.T, f func()) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestSessionCalls(t *testing.T) {
	c, f := newPair(t)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		req := f.expect("initialize")
		if params(req)["protocolVersion"] != float64(1) {
			t.Errorf("protocolVersion = %v", params(req)["protocolVersion"])
		}
		caps := params(req)["clientCapabilities"].(map[string]any)
		if _, ok := caps["fs"]; ok {
			t.Error("initialize offers fs")
		}
		f.reply(req, map[string]any{"protocolVersion": 1})

		req = f.expect("session/new")
		if params(req)["cwd"] != "/work" {
			t.Errorf("cwd = %v", params(req)["cwd"])
		}
		if mcp, ok := params(req)["mcpServers"].([]any); !ok || len(mcp) != 0 {
			t.Errorf("mcpServers = %v", params(req)["mcpServers"])
		}
		if _, ok := params(req)["_meta"]; ok {
			t.Errorf("a session fx names sent _meta %v", params(req)["_meta"])
		}
		f.reply(req, map[string]any{"sessionId": "s1", "configOptions": []any{}})

		req = f.expect("session/resume")
		if params(req)["sessionId"] != "old" || params(req)["cwd"] != "/work" {
			t.Errorf("resume params = %v", params(req))
		}
		f.reply(req, map[string]any{})

		req = f.expect("session/resume")
		f.fail(req, -32002, "Session not found")

		req = f.expect("session/new")
		if meta, _ := params(req)["_meta"].(map[string]any); meta["fx"].(map[string]any)["sessionId"] != "chosen" {
			t.Errorf("new session params = %v", params(req))
		}
		f.reply(req, map[string]any{"sessionId": "chosen"})

		req = f.expect("session/set_config_option")
		if params(req)["configId"] != "model" || params(req)["value"] != "gpt-x" || params(req)["sessionId"] != "s1" {
			t.Errorf("model params = %v", params(req))
		}
		f.reply(req, map[string]any{"configOptions": []any{}})

		req = f.expect("session/set_config_option")
		if params(req)["configId"] != "effort" || params(req)["value"] != "high" {
			t.Errorf("effort params = %v", params(req))
		}
		f.reply(req, map[string]any{"configOptions": []any{}})

		req = f.expect("session/set_config_option")
		f.fail(req, -32602, "Reasoning effort is not available for the active model")
	}()

	within(t, func() {
		ctx := context.Background()

		err := c.Initialize(ctx)
		if err != nil {
			t.Fatal(err)
		}

		id, err := c.NewSession(ctx, "/work", "")
		if err != nil || id != "s1" {
			t.Fatalf("NewSession = %q, %v", id, err)
		}

		err = c.Resume(ctx, "old", "/work")
		if err != nil {
			t.Fatal(err)
		}

		// A session den named is new until its first prompt saves it.
		err = c.Resume(ctx, "chosen", "/work")
		if !NotFound(err) {
			t.Fatalf("Resume of an unsaved session = %v", err)
		}
		id, err = c.NewSession(ctx, "/work", "chosen")
		if err != nil || id != "chosen" {
			t.Fatalf("NewSession = %q, %v", id, err)
		}

		err = c.SetModel(ctx, "s1", "gpt-x", "high")
		if err != nil {
			t.Fatal(err)
		}

		err = c.SetModel(ctx, "s1", "", "ultra")
		var rpcErr *Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != -32602 {
			t.Fatalf("SetModel error = %v", err)
		}
	})
	wg.Wait()
}

func TestPromptStreamsEvents(t *testing.T) {
	c, f := newPair(t)

	go func() {
		req := f.expect("session/prompt")
		p := params(req)
		blocks := p["prompt"].([]any)
		if len(blocks) != 2 {
			t.Errorf("prompt blocks = %v", blocks)
		}
		img := blocks[1].(map[string]any)
		if img["type"] != "image" || img["mimeType"] != "image/png" || img["data"] != "AQID" {
			t.Errorf("image block = %v", img)
		}

		f.update("s1", map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "hmm"}})
		f.text("s1", "Hel")
		f.update("s1", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "name": "bash", "title": "Run ls", "kind": "execute", "status": "pending"})
		f.update("s1", map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed",
			"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "a.txt"}}},
		})
		f.text("s1", "lo")
		f.update("s1", map[string]any{"sessionUpdate": "usage_update", "used": 120, "size": 1000})
		f.update("s1", map[string]any{"sessionUpdate": "notice", "severity": "info", "title": "Heads up"})
		f.update("s1", map[string]any{"sessionUpdate": "session_info_update", "title": "T"})
		f.reply(req, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 7, "outputTokens": 3}})
	}()

	var events []Event
	within(t, func() {
		res, err := c.Prompt(context.Background(), "s1", Prompt{
			Text:   "hi",
			Images: []Image{{MimeType: "image/png", Data: []byte{1, 2, 3}}},
		}, func(e Event) { events = append(events, e) })
		if err != nil {
			t.Fatal(err)
		}
		if res.StopReason != "end_turn" || res.Text != "Hello" || res.InputTokens != 7 || res.OutputTokens != 3 {
			t.Errorf("result = %+v", res)
		}
	})

	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := "thought text tool_call tool_update text usage notice other"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("kinds = %v, want %s", kinds, want)
	}

	call := events[2]
	if call.ToolCallID != "t1" || call.Title != "Run ls" || call.ToolKind != "execute" || call.Status != "pending" {
		t.Errorf("tool call = %+v", call)
	}
	upd := events[3]
	if upd.ToolCallID != "t1" || upd.Status != "completed" || upd.Text != "a.txt" {
		t.Errorf("tool update = %+v", upd)
	}
	if events[5].Used != 120 || events[5].Size != 1000 {
		t.Errorf("usage = %+v", events[5])
	}
	if events[6].Text != "Heads up" || events[6].Status != "info" {
		t.Errorf("notice = %+v", events[6])
	}
	if len(events[0].Raw) == 0 {
		t.Error("event has no Raw")
	}
}

func TestSteer(t *testing.T) {
	c, f := newPair(t)

	type drop struct{ session, key string }
	dropped := make(chan drop, 2)
	c.OnDropped = func(session, key string) { dropped <- drop{session, key} }

	steered := make(chan struct{})
	go func() {
		turn := f.expect("session/prompt")

		// Two steers: the turn takes the first and drops the second.
		first := f.expect("session/prompt")
		meta := params(first)["_meta"].(map[string]any)["fx"].(map[string]any)
		if meta["steer"] != true {
			t.Errorf("steer meta = %v", meta)
		}
		blocks := params(first)["prompt"].([]any)
		if blocks[0].(map[string]any)["text"] != "use tabs" {
			t.Errorf("steer prompt = %v", blocks)
		}
		second := f.expect("session/prompt")
		close(steered)

		f.text("s1", "done")
		f.reply(turn, map[string]any{"stopReason": "end_turn"})
		f.reply(first, map[string]any{"stopReason": "end_turn", "_meta": map[string]any{"fx": map[string]any{"steering": "absorbed"}}})
		f.fail(second, -32600, "any message")
	}()

	within(t, func() {
		ctx := context.Background()
		var res Result
		var err error
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			res, err = c.Prompt(ctx, "s1", Prompt{Text: "go"}, nil)
		}()

		// The steers are written after the turn's request, in this order.
		time.Sleep(20 * time.Millisecond)
		if err := c.Steer(ctx, "s1", "k1", "use tabs"); err != nil {
			t.Error(err)
		}
		if err := c.Steer(ctx, "s1", "k2", "be brief"); err != nil {
			t.Error(err)
		}

		<-steered
		<-finished
		if err != nil || res.Text != "done" {
			t.Errorf("Prompt = %+v, %v", res, err)
		}

		select {
		case d := <-dropped:
			if d.session != "s1" || d.key != "k2" {
				t.Errorf("dropped = %+v", d)
			}
		case <-time.After(2 * time.Second):
			t.Error("no dropped steer")
		}
		select {
		case d := <-dropped:
			t.Errorf("extra dropped steer %+v", d)
		case <-time.After(50 * time.Millisecond):
		}
	})
}

func TestSteerDroppedAfterCancel(t *testing.T) {
	c, f := newPair(t)

	dropped := make(chan string, 1)
	c.OnDropped = func(_, key string) { dropped <- key }

	go func() {
		turn := f.expect("session/prompt")
		steer := f.expect("session/prompt")
		f.reply(turn, map[string]any{"stopReason": "cancelled"})
		f.reply(steer, map[string]any{"stopReason": "cancelled", "_meta": map[string]any{"fx": map[string]any{"steering": "dropped"}}})
	}()

	within(t, func() {
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			c.Prompt(context.Background(), "s1", Prompt{Text: "go"}, nil)
		}()

		time.Sleep(20 * time.Millisecond)
		c.Steer(context.Background(), "s1", "k1", "late")
		<-finished

		if key := <-dropped; key != "k1" {
			t.Errorf("dropped = %q", key)
		}
	})
}

func TestPromptCancelByContext(t *testing.T) {
	c, f := newPair(t)

	go func() {
		turn := f.expect("session/prompt")
		f.text("s1", "partial")

		cancel := f.recv()
		if cancel["method"] != "session/cancel" || cancel["id"] != nil || params(cancel)["sessionId"] != "s1" {
			t.Errorf("cancel = %v", cancel)
		}
		f.reply(turn, map[string]any{"stopReason": "cancelled"})
	}()

	within(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		res, err := c.Prompt(ctx, "s1", Prompt{Text: "go"}, func(e Event) {
			if e.Text == "partial" {
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
		if res.StopReason != "cancelled" || res.Text != "partial" {
			t.Errorf("result = %+v", res)
		}
	})
}

func TestPromptCancelGivesUp(t *testing.T) {
	old := cancelWait
	cancelWait = 50 * time.Millisecond
	defer func() { cancelWait = old }()

	c, f := newPair(t)
	go func() {
		f.expect("session/prompt")
		f.expect("session/cancel")
	}()

	within(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := c.Prompt(ctx, "s1", Prompt{Text: "go"}, nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestServerRequests(t *testing.T) {
	c, f := newPair(t)

	go func() {
		turn := f.expect("session/prompt")

		f.send(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "session/request_permission", "params": map[string]any{
			"sessionId": "s1",
			"options": []any{
				map[string]any{"optionId": "reject_once", "name": "Reject", "kind": "reject_once"},
				map[string]any{"optionId": "allow_once", "name": "Allow once", "kind": "allow_once"},
			},
		}})
		answer := f.recv()
		outcome := answer["result"].(map[string]any)["outcome"].(map[string]any)
		if answer["id"] != float64(100) || outcome["outcome"] != "selected" || outcome["optionId"] != "allow_once" {
			t.Errorf("permission answer = %v", answer)
		}

		f.send(map[string]any{"jsonrpc": "2.0", "id": "x", "method": "session/request_permission", "params": map[string]any{
			"options": []any{map[string]any{"optionId": "no", "kind": "reject_once"}},
		}})
		answer = f.recv()
		if answer["id"] != "x" || answer["error"].(map[string]any)["code"] != float64(-32601) {
			t.Errorf("permission without allow = %v", answer)
		}

		f.send(map[string]any{"jsonrpc": "2.0", "id": 101, "method": "fs/read_text_file", "params": map[string]any{}})
		answer = f.recv()
		if answer["id"] != float64(101) || answer["error"].(map[string]any)["code"] != float64(-32601) {
			t.Errorf("unknown method answer = %v", answer)
		}

		f.reply(turn, map[string]any{"stopReason": "end_turn"})
	}()

	within(t, func() {
		_, err := c.Prompt(context.Background(), "s1", Prompt{Text: "go"}, nil)
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestServerExitsMidPrompt(t *testing.T) {
	c, f := newPair(t)

	go func() {
		f.expect("session/prompt")
		f.text("s1", "par")
		f.out.Close()
	}()

	within(t, func() {
		res, err := c.Prompt(context.Background(), "s1", Prompt{Text: "go"}, nil)
		if !errors.Is(err, ErrClosed) {
			t.Errorf("err = %v", err)
		}
		if res.Text != "par" {
			t.Errorf("text = %q", res.Text)
		}

		select {
		case <-c.Done():
		case <-time.After(time.Second):
			t.Error("Done is not closed")
		}

		_, err = c.NewSession(context.Background(), "/w", "")
		if !errors.Is(err, ErrClosed) {
			t.Errorf("call after exit: %v", err)
		}
		_, err = c.Prompt(context.Background(), "s1", Prompt{Text: "again"}, nil)
		if !errors.Is(err, ErrClosed) {
			t.Errorf("prompt after exit: %v", err)
		}
	})
}

func TestTwoSessionsInterleaved(t *testing.T) {
	c, f := newPair(t)

	go func() {
		a := f.expect("session/prompt")
		b := f.expect("session/prompt")
		sa, sb := params(a)["sessionId"].(string), params(b)["sessionId"].(string)

		f.text(sa, "a1 ")
		f.text(sb, "b1 ")
		f.text(sa, "a2")
		f.text(sb, "b2")
		f.reply(b, map[string]any{"stopReason": "end_turn"})
		f.reply(a, map[string]any{"stopReason": "end_turn"})
	}()

	within(t, func() {
		var wg sync.WaitGroup
		results := map[string]string{}
		var mu sync.Mutex
		for _, s := range []string{"sa", "sb"} {
			wg.Add(1)
			go func() {
				defer wg.Done()

				var seen []string
				res, err := c.Prompt(context.Background(), s, Prompt{Text: "go"}, func(e Event) { seen = append(seen, e.Text) })
				if err != nil {
					t.Error(err)
				}

				mu.Lock()
				results[s] = res.Text + "|" + strings.Join(seen, "")
				mu.Unlock()
			}()
			// The fake reads sa's request first.
			time.Sleep(20 * time.Millisecond)
		}
		wg.Wait()

		if results["sa"] != "a1 a2|a1 a2" || results["sb"] != "b1 b2|b1 b2" {
			t.Errorf("results = %v", results)
		}
	})
}

func TestPromptTwiceInOneSession(t *testing.T) {
	c, f := newPair(t)

	go f.expect("session/prompt")

	within(t, func() {
		go c.Prompt(context.Background(), "s1", Prompt{Text: "go"}, nil)
		time.Sleep(50 * time.Millisecond)

		_, err := c.Prompt(context.Background(), "s1", Prompt{Text: "again"}, nil)
		if err == nil {
			t.Error("a second prompt in one session was accepted")
		}
	})
}

func TestSteerRunsAsOwnTurn(t *testing.T) {
	c, f := newPair(t)

	type turn struct {
		session, key string
		res          Result
	}
	turns := make(chan turn, 1)
	c.OnTurn = func(session, key string, res Result) { turns <- turn{session, key, res} }
	c.OnDropped = func(_, key string) { t.Errorf("dropped %q", key) }

	go func() {
		first := f.expect("session/prompt")
		steer := f.expect("session/prompt")

		// The first turn ends; fx runs the steer as the next turn, whose
		// answer has no steering meta.
		f.reply(first, map[string]any{"stopReason": "end_turn"})
		f.text("s1", "re")
		f.update("s1", map[string]any{"sessionUpdate": "usage_update", "used": 1, "size": 2})
		f.text("s1", "ply")
		f.reply(steer, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 5, "outputTokens": 2}})
	}()

	within(t, func() {
		ctx := context.Background()
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			c.Prompt(ctx, "s1", Prompt{Text: "go"}, nil)
		}()

		time.Sleep(20 * time.Millisecond)
		if !c.Running("s1") {
			t.Error("Running is false during a prompt")
		}
		if err := c.Steer(ctx, "s1", "k1", "also"); err != nil {
			t.Fatal(err)
		}
		<-finished

		got := <-turns
		if got.session != "s1" || got.key != "k1" || got.res.Text != "reply" ||
			got.res.StopReason != "end_turn" || got.res.InputTokens != 5 || got.res.OutputTokens != 2 {
			t.Errorf("turn = %+v", got)
		}
		if c.Running("s1") {
			t.Error("Running is true after the steered turn")
		}
	})
}

func TestPromptBusyDuringSteeredTurn(t *testing.T) {
	c, f := newPair(t)
	c.OnTurn = func(string, string, Result) {}

	release := make(chan struct{})
	go func() {
		first := f.expect("session/prompt")
		steer := f.expect("session/prompt")
		f.reply(first, map[string]any{"stopReason": "end_turn"})
		f.text("s1", "x")
		<-release
		f.reply(steer, map[string]any{"stopReason": "end_turn"})
	}()

	within(t, func() {
		ctx := context.Background()
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			c.Prompt(ctx, "s1", Prompt{Text: "go"}, nil)
		}()

		time.Sleep(20 * time.Millisecond)
		c.Steer(ctx, "s1", "k1", "also")
		<-finished

		deadline := time.Now().Add(2 * time.Second)
		for !c.Running("s1") && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}

		_, err := c.Prompt(ctx, "s1", Prompt{Text: "more"}, nil)
		if !errors.Is(err, ErrBusy) {
			t.Errorf("err = %v", err)
		}

		close(release)
		for c.Running("s1") && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if c.Running("s1") {
			t.Error("still running")
		}
	})
}

func TestSteerWhenIdle(t *testing.T) {
	c, _ := newPair(t)

	err := c.Steer(context.Background(), "s1", "k1", "hello")
	if !errors.Is(err, ErrIdle) {
		t.Errorf("err = %v", err)
	}
}

func TestSteerDroppedWhenServerExits(t *testing.T) {
	c, f := newPair(t)

	dropped := make(chan string, 1)
	c.OnDropped = func(_, key string) { dropped <- key }

	go func() {
		f.expect("session/prompt")
		f.expect("session/prompt")
		f.out.Close()
	}()

	within(t, func() {
		go c.Prompt(context.Background(), "s1", Prompt{Text: "go"}, nil)

		time.Sleep(20 * time.Millisecond)
		err := c.Steer(context.Background(), "s1", "k1", "lost")
		if err != nil {
			t.Fatal(err)
		}

		if key := <-dropped; key != "k1" {
			t.Errorf("dropped = %q", key)
		}
	})
}
