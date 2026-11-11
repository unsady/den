// Package mapper asks a model the same question about many items, one short
// request per item, for classification and extraction that would otherwise
// fill an agent's context.
//
// Every request opens with the same instructions, so the provider caches
// them, and carries one item, so a request stays small however many items
// there are.
package mapper

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Options say where to send the items and what to ask.
type Options struct {
	// URL is the provider's chat completions endpoint, or with Responses
	// set, a ChatGPT subscription's Codex responses endpoint.
	URL       string
	Responses bool
	Model     string
	// Instructions are the question each item is answered against.
	Instructions string
	Workers      int
	// Resume keeps answers already in the output file and asks only for the
	// rest.
	Resume bool
	Client *http.Client
	// Wait is how long to wait before retrying a failed request; tests
	// shorten it.
	Wait time.Duration
	// Progress, when set, is told how far the run is every Every, when that
	// changed.
	Progress func(Status)
	Every    time.Duration
}

// Result is the answer to one item: one line of the output file.
type Result struct {
	// Line is the item's line in the input, from 1.
	Line int `json:"line"`
	// ID is the item's own "id" field, when it has one.
	ID     json.RawMessage `json:"id,omitempty"`
	Answer json.RawMessage `json:"answer,omitempty"`
	// Text is a reply that was not JSON.
	Text    string  `json:"text,omitempty"`
	Error   string  `json:"error,omitempty"`
	Input   int     `json:"input_tokens,omitempty"`
	Cached  int     `json:"cached_tokens,omitempty"`
	Output  int     `json:"output_tokens,omitempty"`
	Cost    float64 `json:"cost,omitempty"`
	Seconds float64 `json:"seconds"`
}

// Status is how far a run is.
type Status struct {
	Done, Failed, Total int
	Cost                float64
}

// Summary is what a run did.
type Summary struct {
	Items    int     `json:"items"`
	Kept     int     `json:"kept,omitempty"`
	Answered int     `json:"answered"`
	Failed   int     `json:"failed"`
	Minutes  float64 `json:"minutes"`
	Input    int     `json:"input_tokens"`
	Cached   int     `json:"cached_tokens"`
	Output   int     `json:"output_tokens"`
	Cost     float64 `json:"cost"`
	// Values counts the answers' short top-level fields, such as a verdict.
	Values map[string]map[string]int `json:"values,omitempty"`
}

const reply = "\n\nAnswer with one JSON value only: no prose and no code fence."

// Run answers each line of in and writes the results to out, one JSON line
// per item, in the order they finish.
func Run(ctx context.Context, opts Options, in io.Reader, out string) (Summary, error) {
	var items []item
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 1<<20), 64<<20)
	for n := 1; scan.Scan(); n++ {
		if line := bytes.TrimSpace(scan.Bytes()); len(line) > 0 {
			items = append(items, item{n, bytes.Clone(line)})
		}
	}
	if err := scan.Err(); err != nil {
		return Summary{}, err
	}

	kept := map[int]Result{}
	if opts.Resume {
		kept = previous(out)
	}
	file, err := os.Create(out)
	if err != nil {
		return Summary{}, err
	}
	defer file.Close()

	sum := Summary{Items: len(items)}
	var results []Result
	var mu sync.Mutex
	write := func(r Result) error {
		mu.Lock()
		defer mu.Unlock()
		results = append(results, r)
		line, _ := json.Marshal(r)
		_, err := file.Write(append(line, '\n'))
		return err
	}

	var todo []item
	for _, it := range items {
		if r, ok := kept[it.line]; ok {
			sum.Kept++
			if err := write(r); err != nil {
				return sum, err
			}
			continue
		}
		todo = append(todo, it)
	}

	start := time.Now()
	stop := make(chan struct{})
	defer close(stop)
	if opts.Progress != nil && opts.Every > 0 {
		go progress(opts.Progress, opts.Every, stop, &mu, &results, len(items))
	}

	ask := asker{opts: opts, system: opts.Instructions + reply}
	ask.session = "den-map-" + digest(ask.system)
	queue := make(chan item)
	var wg sync.WaitGroup
	var failed error
	for n := range max(1, opts.Workers) {
		// Each worker keeps a Codex cache key of its own, so the requests
		// that follow one another share a cache.
		worker := ask
		worker.key = fmt.Sprintf("%s-%d", ask.session, n)
		wg.Go(func() {
			for it := range queue {
				if err := write(worker.answer(ctx, it)); err != nil {
					mu.Lock()
					failed = err
					mu.Unlock()
				}
			}
		})
	}
	for _, it := range todo {
		if ctx.Err() != nil {
			break
		}
		queue <- it
	}
	close(queue)
	wg.Wait()

	sum.Minutes = float64(int(time.Since(start).Minutes()*10)) / 10
	for _, r := range results {
		if r.Error != "" {
			sum.Failed++
		} else {
			sum.Answered++
		}
		sum.Input += r.Input
		sum.Cached += r.Cached
		sum.Output += r.Output
		sum.Cost += r.Cost
	}
	sum.Values = values(results)
	if failed == nil {
		failed = ctx.Err()
	}
	return sum, failed
}

// progress reports how many items have an answer, when that changed.
func progress(report func(Status), every time.Duration, stop <-chan struct{}, mu *sync.Mutex, results *[]Result, total int) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	last := -1
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}

		mu.Lock()
		s := Status{Done: len(*results), Total: total}
		for _, r := range *results {
			if r.Error != "" {
				s.Failed++
			}
			s.Cost += r.Cost
		}
		mu.Unlock()

		if s.Done != last {
			last = s.Done
			report(s)
		}
	}
}

type item struct {
	line int
	data []byte
}

type asker struct {
	opts    Options
	system  string
	session string
	// key is the worker's Codex cache key.
	key string
}

// answer asks about one item, retrying a failed request and a reply that
// is not JSON.
func (a asker) answer(ctx context.Context, it item) (r Result) {
	r = Result{Line: it.line, ID: id(it.data)}
	start := time.Now()
	defer func() { r.Seconds = float64(int(time.Since(start).Seconds()*10)) / 10 }()

	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				r.Error = ctx.Err().Error()
				return r
			case <-time.After(a.opts.Wait * time.Duration(attempt)):
			}
		}

		text, use, retry, err := a.ask(ctx, content(it.data))
		r.Input += use.Prompt
		r.Cached += use.Details.Cached
		r.Output += use.Completion
		r.Cost += use.cost()
		if err != nil {
			r.Error = err.Error()
			if retry {
				continue
			}
			return r
		}

		if answer := extract(text); answer != nil {
			r.Answer, r.Text, r.Error = answer, "", ""
			return r
		}
		r.Text, r.Error = text, "the reply is not JSON"
	}
	return r
}

// usage is what an OpenAI-style provider reports for one request.
type usage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Details    struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	Cost     float64 `json:"cost"`
	OpenCode struct {
		ListCost float64 `json:"list_cost"`
	} `json:"opencode"`
}

// cost is the request's price in dollars; OpenCode's subscription reports
// its list price apart.
func (u usage) cost() float64 {
	if u.Cost > 0 {
		return u.Cost
	}
	return u.OpenCode.ListCost
}

func (a asker) ask(ctx context.Context, item string) (text string, use usage, retry bool, err error) {
	body, _ := json.Marshal(map[string]any{
		"model":       a.opts.Model,
		"temperature": 0,
		"messages": []map[string]any{
			{"role": "system", "content": a.instructions()},
			{"role": "user", "content": item},
		},
	})
	if a.opts.Responses {
		// Codex takes only streamed requests that it does not store, and
		// chooses the output limit itself. Low effort suits one item. It
		// reuses its cache of the instructions only for requests that share
		// a prompt_cache_key, and only once they reach about 1,024 tokens.
		body, _ = json.Marshal(map[string]any{
			"model":            a.opts.Model,
			"prompt_cache_key": a.key,
			"store":            false,
			"stream":           true,
			"instructions":     a.system,
			"input":            []map[string]any{{"role": "user", "content": []map[string]string{{"type": "input_text", "text": item}}}},
			"reasoning":        map[string]string{"effort": "low"},
			"text":             map[string]string{"verbosity": "low"},
		})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.opts.URL, bytes.NewReader(body))
	if err != nil {
		return "", use, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	// One name for every request lets the provider route them alike and
	// reuse its cache of the instructions.
	req.Header.Set("X-Opencode-Session", a.session)
	if a.opts.Responses {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "den")
		req.Header.Set("session-id", a.session)
	}

	client := a.opts.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", use, ctx.Err() == nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", use, true, err
	}
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return "", use, retry, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 300)])))
	}

	if a.opts.Responses {
		return events(data)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", out.Usage, true, errors.New("the provider's answer has no reply")
	}
	return out.Choices[0].Message.Content, out.Usage, false, nil
}

// events reads a streamed Codex response: its text and usage, or why it
// failed.
func events(stream []byte) (text string, use usage, retry bool, err error) {
	var deltas strings.Builder
	for line := range bytes.SplitSeq(stream, []byte("\n")) {
		data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data: "))
		if !ok {
			continue
		}
		var e struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Text     string `json:"text"`
			Message  string `json:"message"`
			Response struct {
				Usage struct {
					Input   int `json:"input_tokens"`
					Output  int `json:"output_tokens"`
					Details struct {
						Cached int `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &e) != nil {
			continue
		}
		switch e.Type {
		case "response.output_text.delta":
			deltas.WriteString(e.Delta)
		case "response.output_text.done":
			text = e.Text
		case "response.completed":
			u := e.Response.Usage
			use.Prompt, use.Completion, use.Details.Cached = u.Input, u.Output, u.Details.Cached
		case "response.failed", "error":
			msg := e.Message
			if e.Response.Error != nil {
				msg = e.Response.Error.Message
			}
			return "", use, true, fmt.Errorf("codex: %s", msg)
		}
	}
	if text == "" {
		text = deltas.String()
	}
	if text == "" {
		return "", use, true, errors.New("the provider's answer has no reply")
	}
	return text, use, false, nil
}

// instructions are the system message. Claude caches only up to a marked
// block, and OpenCode otherwise marks the end of the whole request, which
// writes each item to the cache at a higher price and never reads it back.
func (a asker) instructions() any {
	if !strings.Contains(strings.ToLower(a.opts.Model), "claude") {
		return a.system
	}
	return []map[string]any{{"type": "text", "text": a.system, "cache_control": map[string]string{"type": "ephemeral"}}}
}

// content is what the model reads for an item: a JSON string's text, or
// the line itself.
func content(line []byte) string {
	var s string
	if json.Unmarshal(line, &s) == nil {
		return s
	}
	return string(line)
}

func id(line []byte) json.RawMessage {
	var obj struct {
		ID json.RawMessage `json:"id"`
	}
	json.Unmarshal(line, &obj)
	return obj.ID
}

// extract finds the JSON value in a reply, which models sometimes wrap in a
// code fence or a sentence.
func extract(text string) json.RawMessage {
	text = strings.TrimSpace(text)
	if json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}
	for _, pair := range []string{"{}", "[]"} {
		first, last := strings.IndexByte(text, pair[0]), strings.LastIndexByte(text, pair[1])
		if first >= 0 && last > first && json.Valid([]byte(text[first:last+1])) {
			return json.RawMessage(text[first : last+1])
		}
	}
	return nil
}

// previous reads the answered items of an earlier run's output.
func previous(path string) map[int]Result {
	kept := map[int]Result{}
	data, err := os.ReadFile(path)
	if err != nil {
		return kept
	}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		var r Result
		if json.Unmarshal(line, &r) == nil && r.Line > 0 && r.Error == "" {
			kept[r.Line] = r
		}
	}
	return kept
}

// values counts the answers' short top-level strings and booleans that take
// few values, so a summary shows such as how many items each verdict got,
// and not each item's reason.
func values(results []Result) map[string]map[string]int {
	counts := map[string]map[string]int{}
	for _, r := range results {
		var obj map[string]any
		if json.Unmarshal(r.Answer, &obj) != nil {
			continue
		}
		for k, v := range obj {
			var s string
			switch v := v.(type) {
			case string:
				s = v
			case bool:
				s = fmt.Sprint(v)
			default:
				continue
			}
			if counts[k] == nil {
				counts[k] = map[string]int{}
			}
			counts[k][s]++
		}
	}
	for k, c := range counts {
		for v := range c {
			if len(v) > 40 {
				c = nil
				break
			}
		}
		if len(c) == 0 || len(c) > 12 {
			delete(counts, k)
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:24]
}
