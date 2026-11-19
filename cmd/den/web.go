package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/client"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/fetch"
	"github.com/unsady/den/internal/search"
)

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

	req := fetch.Request{Secret: *secret, Method: *method, URL: rest[0], Header: header, Session: os.Getenv("DEN_SESSION")}
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
		fmt.Fprintln(os.Stderr, "den asked the user to allow this request in Telegram. Once they answer, den sends it and tells you the service's answer in a new message, so you need not wait. Do not send it again. "+approve.Quiet)
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
