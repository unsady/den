// Command den is a small home for a personal fx agent: a scheduler,
// notifications, and an archive of past sessions.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
	_ "time/tzdata"

	"github.com/unsady/den/internal/client"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/daemon"
	"github.com/unsady/den/internal/fetch"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/search"
	"github.com/unsady/den/internal/seed"
	"github.com/unsady/den/internal/sessions"
)

const usage = `den: a small home for a personal fx agent.

Usage:
  den job add --schedule <spec> [--name <name>] [--slot main|second] [--silent] <prompt>
  den job ls [--json]
  den job slot <id|name> main|second
                                Run the job on the chat's main or second model
  den job rm <id|name>
  den job run <id|name>
  den job log <id|name> [-n <count>]
  den notify <text>             Send a message to the user; reads stdin without text
  den send [--caption <text>] <file>
                                Send the user a file; an image goes as a photo
  den fetch --secret <name> [-X <method>] [-H "<name>: <value>"]... [-d <body>|@<file>|@-] [-i] <url>
                                Send an HTTP request with a secret den holds,
                                such as github; prints the response body
  den search [-n <count>] [--json] <query>
                                Search the web through the services den has keys for
  den map -p <instructions>|@<file> [--model <provider/model>] [--out <file>] [--workers 6] [--resume] <items.jsonl>
                                Ask a cheap model about each line in its own short
                                request and write one JSON answer per item
  den sessions export [--force] Write fx sessions as markdown into the archive
  den sessions recent [--since 24h] [--after <file>]
                                Export, then list sessions that changed since then
  den secret set <name> | ls | rm <name>
                                Keep a secret, read from stdin (root only)
  den stats [--since 24h] [--json]
                                How each model provider answered: silence before the
                                first token, stalls, speed, and failures
  den service ls                The APIs den sends a token to, for den fetch --secret
  den service add [--header <name>] [--ask writes|never|always] [--about <text>] <name> <https-url>
                                Add an API with its token on stdin (root only)
  den service rm <name>
  den login [--model <id>] [--base-url <url>] <provider>
                                Sign the agent in to a model provider with the key
                                on stdin, which stays with den (root only)
  den connect google | mail     Connect Google over stdio, or a mailbox from the
                                account on stdin (root only)
  den disconnect google | mail
  den serve                     Run the daemon: scheduler, notifications, and chat
  den init                      Create the agent's space if it is missing

Schedules:
  in 30m | in 2h               once, that long from now
  2026-11-10 09:00             once, at that local time
  every 2h                     repeatedly, at that interval
  0 9 * * 1-5 | @daily         repeatedly, by cron expression

A job runs fx in a fresh session in the space. Its final reply is sent to
the user unless it is SILENT or the job is --silent.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "den:", err)
		os.Exit(1)
	}
}

// host runs the commands of den on the host, which drive den in its
// container. It is nil elsewhere.
var host func(args []string) error

// version is den's release, set when a release is built.
var version = "dev"

func run(args []string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("den " + version)
		return nil
	}
	if host != nil {
		return host(args)
	}
	cfg := config.Load()
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	switch args[0] {
	case "job", "jobs":
		return jobCommand(cfg, args[1:])
	case "notify":
		return notifyCommand(cfg, args[1:])
	case "send":
		return sendCommand(cfg, args[1:])
	case "fetch":
		return fetchCommand(cfg, args[1:])
	case "search":
		return searchCommand(cfg, args[1:])
	case "map":
		return mapCommand(cfg, args[1:])
	case "sessions":
		return sessionsCommand(cfg, args[1:])
	case "secret", "secrets":
		return secretCommand(cfg, args[1:])
	case "service", "services":
		return serviceCommand(cfg, args[1:])
	case "stats":
		return statsCommand(cfg, args[1:])
	case "login":
		return loginCommand(cfg, args[1:])
	case "connect":
		return connectCommand(cfg, args[1:])
	case "disconnect":
		return disconnectCommand(cfg, args[1:])
	case "serve":
		return daemon.Serve(cfg)
	case "init":
		return seed.Init(cfg)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q; run `den help`", args[0])
}

func jobCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("job needs a subcommand: add, ls, rm, run, or log")
	}
	c := client.New(cfg)
	sub, args := args[0], args[1:]

	switch sub {
	case "add":
		fs := flag.NewFlagSet("job add", flag.ContinueOnError)
		var req daemon.AddRequest
		fs.StringVar(&req.Schedule, "schedule", "", "when the job runs")
		fs.StringVar(&req.Name, "name", "", "a name to refer to the job by")
		fs.StringVar(&req.Slot, "slot", jobs.Main, "the chat's model the job runs on: main or second")
		fs.BoolVar(&req.Silent, "silent", false, "keep the reply in the job log instead of sending it")
		rest, err := parse(fs, args)
		if err != nil {
			return err
		}
		if req.Schedule == "" {
			return fmt.Errorf("job add needs --schedule")
		}
		req.Prompt = strings.Join(rest, " ")

		var j jobs.Job
		if err := c.Do(http.MethodPost, "/jobs", req, &j); err != nil {
			return err
		}
		fmt.Printf("added job %s, next run %s\n", label(j), when(j.NextRun))
		return nil

	case "ls", "list":
		fs := flag.NewFlagSet("job ls", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		if _, err := parse(fs, args); err != nil {
			return err
		}

		var list []jobs.Job
		if err := c.Do(http.MethodGet, "/jobs", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(list)
		}
		if len(list) == 0 {
			fmt.Println("no jobs")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tNEXT\tSCHEDULE\tMODEL\tLAST\tPROMPT")
		for _, j := range list {
			last := "-"
			if j.LastRun != nil {
				last = j.LastStatus + " " + when(*j.LastRun)
			}
			slot := jobs.Main
			if j.Slot != "" {
				slot = j.Slot
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, dash(j.Name), when(j.NextRun), j.Schedule, slot, last, oneLine(j.Prompt, 50))
		}
		return w.Flush()

	case "slot":
		if len(args) != 2 {
			return fmt.Errorf("job slot takes a job and main or second")
		}
		var j jobs.Job
		if err := c.Do(http.MethodPatch, "/jobs/"+url.PathEscape(args[0]), daemon.SlotRequest{Slot: args[1]}, &j); err != nil {
			return err
		}
		fmt.Printf("job %s runs on the %s model\n", label(j), args[1])
		return nil

	case "rm", "remove":
		ref, err := oneArg("job rm", args)
		if err != nil {
			return err
		}
		var j jobs.Job
		if err := c.Do(http.MethodDelete, "/jobs/"+url.PathEscape(ref), nil, &j); err != nil {
			return err
		}
		fmt.Printf("removed job %s\n", label(j))
		return nil

	case "run":
		ref, err := oneArg("job run", args)
		if err != nil {
			return err
		}
		var j jobs.Job
		if err := c.Do(http.MethodPost, "/jobs/"+url.PathEscape(ref)+"/run", nil, &j); err != nil {
			return err
		}
		fmt.Printf("started job %s; see `den job log %s`\n", label(j), j.ID)
		return nil

	case "log":
		fs := flag.NewFlagSet("job log", flag.ContinueOnError)
		n := fs.Int("n", 3, "how many runs to show")
		rest, err := parse(fs, args)
		if err != nil {
			return err
		}
		ref, err := oneArg("job log", rest)
		if err != nil {
			return err
		}

		var runs []jobs.Run
		if err := c.Do(http.MethodGet, fmt.Sprintf("/jobs/%s/runs?n=%d", url.PathEscape(ref), *n), nil, &runs); err != nil {
			return err
		}
		if len(runs) == 0 {
			fmt.Println("no runs yet")
			return nil
		}
		for _, r := range runs {
			printRun(r)
		}
		return nil
	}
	return fmt.Errorf("unknown job subcommand %q", sub)
}

func notifyCommand(cfg config.Config, args []string) error {
	text := strings.Join(args, " ")
	if strings.TrimSpace(text) == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("notify needs text, as arguments or on stdin")
	}

	if err := client.New(cfg).Do(http.MethodPost, "/notify", daemon.NotifyRequest{Text: text}, nil); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}

func sendCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	caption := fs.String("caption", "", "text under the file")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("send needs one file")
	}

	data, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}
	if len(data) > notify.MaxUpload {
		return fmt.Errorf("%s is over Telegram's 50 MB limit for bots", rest[0])
	}
	req := daemon.SendRequest{Name: filepath.Base(rest[0]), Caption: *caption, Data: data}
	if err := client.New(cfg).Do(http.MethodPost, "/send", req, nil); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}

func searchCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	n := fs.Int("n", 8, "how many results, up to 20")
	asJSON := fs.Bool("json", false, "print JSON")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(rest, " "))
	if query == "" {
		return fmt.Errorf("search needs a query")
	}

	var reply search.Reply
	path := "/search?" + url.Values{"q": {query}, "n": {fmt.Sprint(*n)}}.Encode()
	if err := client.New(cfg).Do(http.MethodGet, path, nil, &reply); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(reply)
	}
	for i, r := range reply.Results {
		fmt.Printf("%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if snippet := strings.Join(strings.Fields(r.Snippet), " "); snippet != "" {
			fmt.Printf("   %s\n", snippet)
		}
	}
	if len(reply.Results) == 0 {
		fmt.Println("no results")
	}
	fmt.Fprintf(os.Stderr, "(via %s)\n", reply.Service)
	return nil
}

// headers collects repeated -H flags.
type headers map[string]string

func (h headers) String() string { return "" }

func (h headers) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("a header looks like \"Name: value\"")
	}
	h[strings.TrimSpace(name)] = strings.TrimSpace(value)
	return nil
}

func fetchCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	secret := fs.String("secret", "", "the secret to send, such as github")
	method := fs.String("X", "", "the method; POST when there is a body, else GET")
	data := fs.String("d", "", "the body, @file to read a file, or @- for stdin")
	include := fs.Bool("i", false, "print the status and headers before the body")
	header := headers{}
	fs.Var(header, "H", "a header, repeatable")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || *secret == "" {
		return fmt.Errorf("fetch needs --secret and one URL")
	}

	req := fetch.Request{Secret: *secret, Method: *method, URL: rest[0], Header: header, Turn: os.Getenv("DEN_TURN")}
	switch {
	case *data == "@-":
		req.Body, err = io.ReadAll(os.Stdin)
	case strings.HasPrefix(*data, "@"):
		req.Body, err = os.ReadFile(strings.TrimPrefix(*data, "@"))
	default:
		req.Body = []byte(*data)
	}
	if err != nil {
		return err
	}
	if len(req.Body) > 0 {
		if req.Method == "" {
			req.Method = http.MethodPost
		}
		if _, ok := header["Content-Type"]; !ok && json.Valid(req.Body) {
			header["Content-Type"] = "application/json"
		}
	}

	// A write the user must allow does not wait for them: den sends it
	// once they do.
	var resp fetch.Response
	if err := client.New(cfg).Wait(6*time.Minute).Do(http.MethodPost, "/fetch", req, &resp); err != nil {
		return err
	}
	if resp.Waiting {
		fmt.Fprintln(os.Stderr, "den asked the user to allow this request in Telegram. Once they answer, den sends it and tells you the service's answer in a new message, so you need not wait: go on, or end your turn. Do not send it again.")
		return nil
	}
	if *include {
		fmt.Printf("HTTP %d\n", resp.Status)
		for k, v := range resp.Header {
			fmt.Printf("%s: %s\n", k, v)
		}
		fmt.Println()
	}
	_, _ = os.Stdout.Write(resp.Body)
	if resp.Status >= 400 {
		return fmt.Errorf("HTTP %d", resp.Status)
	}
	return nil
}

func sessionsCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("sessions needs a subcommand: export or recent")
	}

	switch args[0] {
	case "export":
		fs := flag.NewFlagSet("sessions export", flag.ContinueOnError)
		force := fs.Bool("force", false, "rewrite sessions that have not changed")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}

		stats, err := sessions.Export(cfg.Sessions, cfg.Archive, *force)
		if err != nil {
			return err
		}
		fmt.Printf("exported %d sessions to %s (%d unchanged, %d empty)\n", stats.Written, cfg.Archive, stats.Unchanged, stats.Skipped)
		return nil

	case "recent":
		fs := flag.NewFlagSet("sessions recent", flag.ContinueOnError)
		since := fs.Duration("since", 24*time.Hour, "how far back to look")
		after := fs.String("after", "", "list sessions changed after this file's modification time")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}

		cutoff := time.Now().Add(-*since)
		if *after != "" {
			if info, err := os.Stat(*after); err == nil {
				cutoff = info.ModTime()
			}
		}

		if _, err := sessions.Export(cfg.Sessions, cfg.Archive, false); err != nil {
			return err
		}
		paths, err := sessions.Recent(cfg.Sessions, cfg.Archive, cutoff)
		if err != nil {
			return err
		}
		for _, p := range paths {
			fmt.Println(p)
		}
		return nil
	}
	return fmt.Errorf("unknown sessions subcommand %q", args[0])
}

// parse reads flags anywhere among args, so `den job log reflect -n 5`
// works. Everything after "--" is a positional argument.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func oneArg(command string, args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%s needs one job id or name", command)
	}
	return args[0], nil
}

func printRun(r jobs.Run) {
	fmt.Printf("%s  %s  %s", r.Started.Local().Format("2006-01-02 15:04"), r.Status(), r.Duration)
	if r.Model != "" {
		fmt.Printf("  %s", r.Model)
	}
	if r.SessionID != "" {
		fmt.Printf("  session %s", r.SessionID)
	}
	if r.Delivered {
		fmt.Print("  delivered")
	}
	fmt.Println()

	if r.Output != "" {
		fmt.Println(indent(r.Output))
	}
	if r.Error != "" {
		fmt.Println(indent("error: " + r.Error))
		if r.Stderr != "" {
			fmt.Println(indent(r.Stderr))
		}
	}
	fmt.Println()
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func label(j jobs.Job) string {
	if j.Name != "" {
		return fmt.Sprintf("%s (%s)", j.Name, j.ID)
	}
	return j.ID
}

func when(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}
