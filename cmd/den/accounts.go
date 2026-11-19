package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/unsady/den/internal/codex"
	"github.com/unsady/den/internal/google"
	"github.com/unsady/den/internal/grok"
	"github.com/unsady/den/internal/mail"
)

// account is a way to pay for den's model that setup offers by name, so a
// new user picks what they have rather than a provider and a model id.
type account struct {
	name  string
	about string
	// provider is the models.dev id, codex, grok or claude for a
	// subscription, or "" for one the user picks from models.dev.
	provider string
	// model is the one den suggests; /model in Telegram changes it.
	model string
	// keys is where the user creates an API key.
	keys string
}

var accounts = []account{
	{"OpenCode Go", "a monthly plan with many open models", "opencode-go", "deepseek-v4-pro", "https://opencode.ai/auth"},
	{"OpenRouter", "pay as you go, almost every model", "openrouter", "deepseek/deepseek-v4-pro", "https://openrouter.ai/settings/keys"},
	{"DeepSeek", "pay as you go, DeepSeek's own API", "deepseek", "deepseek-v4-pro", "https://platform.deepseek.com/api_keys"},
	{"Anthropic", "pay as you go, Claude models", "anthropic", "claude-sonnet-5-5", "https://platform.claude.com/settings/keys"},
	{"xAI", "pay as you go, Grok models", "xai", "grok-4.7", "https://console.x.ai"},
	{"Vercel AI Gateway", "pay as you go, models of many providers", "vercel", "deepseek/deepseek-v4-pro", "https://vercel.com/ai-gateway"},
	{"ChatGPT Plus or Pro", "your subscription, signed in in the browser", "codex", "", ""},
	{"SuperGrok", "your Grok subscription, signed in in the browser", "grok", "", ""},
	{"Claude Pro or Max", "your subscription, through Claude Code's token", "claude", "", ""},
	{"Another provider", "any of models.dev's", "", "", ""},
}

// chooseAccount asks which account pays for den's model and connects it.
func chooseAccount(c hostConfig) error {
	choices := make([]choice, len(accounts))
	for i, a := range accounts {
		choices[i] = choice{label: a.name, about: a.about, value: strconv.Itoa(i)}
	}
	answer, err := choose("Which account pays for den's model?", choices, "0")
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(answer)
	a := accounts[n]

	switch a.provider {
	case "codex":
		if err := connectHost(c, "codex", codex.Redirect, "ChatGPT"); err != nil {
			return err
		}
		done("ChatGPT connected; /model in Telegram picks its model")
		return nil
	case "grok":
		if err := connectHost(c, "grok", grok.Redirect, "Grok"); err != nil {
			return err
		}
		done("Grok connected; /model in Telegram picks its model")
		return nil
	case "claude":
		if err := connectClaude(c); err != nil {
			return err
		}
		note("/model in Telegram picks Claude's model")
		return nil
	case "":
		cat, err := catalog()
		if err != nil {
			return err
		}
		provider, err := choose("Provider", providerChoices(cat), "")
		if err != nil {
			return err
		}
		return login(c, provider, "", "")
	}
	return login(c, a.provider, "", "")
}

func secrets(c hostConfig) map[string]bool {
	out, _ := c.engine().command("exec", c.Name, "den", "secret", "ls").Output()
	have := map[string]bool{}
	for _, name := range strings.Fields(string(out)) {
		have[name] = true
	}
	return have
}

func hostLogin(c hostConfig, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	model := fs.String("model", "", "make this model den's default")
	baseURL := fs.String("base-url", "", "the provider's address, when models.dev has none")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("login takes one provider id from models.dev, such as opencode-go")
	}
	return login(c, rest[0], *model, *baseURL)
}

// login adds a provider. Its models are chosen with /model in Telegram, so
// it asks for a model only when den has no provider yet and needs one.
func login(c hostConfig, provider, model, baseURL string) error {
	if model == "" && !hasProvider(secrets(c)) {
		var err error
		if model, err = pickModel(provider); err != nil {
			return err
		}
	}
	name := provider
	if p, ok := loadedCatalog[provider]; ok && p.Name != "" {
		name = p.Name
	}
	for _, a := range accounts {
		if a.provider == provider {
			name = a.name
			if a.keys != "" {
				note("Create a key at " + a.keys)
			}
		}
	}
	key, err := ask("API key for "+name, true)
	if err != nil {
		return err
	}

	args := []string{"login"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if baseURL != "" {
		args = append(args, "--base-url", baseURL)
	}
	if err := step("Signing in to "+name, func(context.Context) error { return insideQuiet(c, key, append(args, provider)...) }); err != nil {
		return err
	}
	done("Signed in to %s", name)
	if model != "" {
		done("Model %s; /model in Telegram changes it", model)
	} else {
		note("Its models are in /model in Telegram; the default model stays as it was.")
	}
	return nil
}

func hostConnect(c hostConfig, args []string) error {
	if len(args) == 0 {
		return errors.New("connect google, codex, grok, claude, or mail")
	}
	switch args[0] {
	case "mail":
		return connectMail(c)
	case "google":
		if len(args) > 1 {
			client, err := os.ReadFile(args[1])
			if err != nil {
				return err
			}
			if err := inside(c, string(client), "secret", "set", google.ClientFile); err != nil {
				return err
			}
		} else if !secrets(c)[google.ClientFile] {
			return errors.New("den needs your OAuth client first: den connect google <the client JSON from Google Cloud>")
		}
		return connectHost(c, "google", google.Redirect, "Google")
	case "codex":
		return connectHost(c, "codex", codex.Redirect, "ChatGPT")
	case "grok":
		return connectHost(c, "grok", grok.Redirect, "Grok")
	case "claude":
		return connectClaude(c)
	}
	return fmt.Errorf("den connects google, codex, grok, claude, or mail, not %q", args[0])
}

// connectClaude takes the token that `claude setup-token` makes. Its
// sign-in is a terminal screen that only works here, so the user runs it
// and pastes the token, which goes to den in the container on stdin.
func connectClaude(c hostConfig) error {
	note("Claude signs in with a token: run `claude setup-token` from Claude Code, sign in, and copy the token it prints.")
	if _, err := exec.LookPath("claude"); err != nil {
		note("Claude Code is not on this computer: install it, or run `claude setup-token` on another one.")
	} else {
		answer, err := choose("Run claude setup-token now?", []choice{{label: "Yes", value: "yes"}, {label: "No, I have a token", value: "no"}}, "yes")
		if err != nil {
			return err
		}
		if answer == "yes" {
			cmd := exec.Command("claude", "setup-token")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("claude setup-token: %v", err)
			}
		}
	}

	token, err := ask("Token: ", true)
	if err != nil {
		return err
	}
	if err := insideQuiet(c, token, "connect", "claude"); err != nil {
		return err
	}
	done("Claude connected")
	if bytes, ok := parseSize(c.Memory); ok && bytes < 2<<30 {
		note(fmt.Sprintf("Claude runs a process of about 250 MB per conversation. den's container has %s; for chats and jobs at once give it 2G: set \"memory\": \"2G\" in %s and run den restart.", c.Memory, configPath()))
	}
	return nil
}

// parseSize reads a container memory size such as 1G, 1024M, or 512m. It
// says false for a format it does not know.
func parseSize(s string) (int64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "b")

	unit := int64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		unit = 1 << 10
	case strings.HasSuffix(s, "m"):
		unit = 1 << 20
	case strings.HasSuffix(s, "g"):
		unit = 1 << 30
	}
	if unit > 1 {
		s = s[:len(s)-1]
	}

	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return int64(n * float64(unit)), true
}

func connectMail(c hostConfig) error {
	address, err := ask("Email address: ", false)
	if err != nil {
		return err
	}
	password, err := ask("App password: ", true)
	if err != nil {
		return err
	}
	text := "address=" + address + "\npassword=" + password + "\n"
	if _, err := mail.Parse(text); err != nil {
		imap, err := ask("IMAP server, host:993: ", false)
		if err != nil {
			return err
		}
		smtp, err := ask("SMTP server, host:465 or host:587: ", false)
		if err != nil {
			return err
		}
		text += "imap=" + imap + "\nsmtp=" + smtp + "\n"
	}
	if err := step("Checking the mailbox", func(context.Context) error { return insideQuiet(c, text, "connect", "mail") }); err != nil {
		return err
	}
	done("Mail connected: %s", address)
	return nil
}

// connectHost signs in in this machine's browser. den in the container
// keeps the PKCE verifier and does the exchange; this side opens the
// browser and catches the loopback redirect, which only works here.
func connectHost(c hostConfig, service, back, name string) error {
	cmd := c.engine().command("exec", "-i", c.Name, "den", "connect", service)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	redirect, _ := url.Parse(back)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(stdin, "http://"+redirect.Host+r.URL.RequestURI())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>den</title><p style=\"font:18px system-ui;margin:20vh auto;max-width:30rem\">Done. Return to the terminal.</p>")
	})}
	defer srv.Shutdown(context.Background())
	// localhost may reach either loopback address.
	addrs := []string{redirect.Host}
	if redirect.Hostname() == "localhost" {
		addrs = []string{"127.0.0.1:" + redirect.Port(), "[::1]:" + redirect.Port()}
	}
	listening := false
	for _, addr := range addrs {
		if l, err := net.Listen("tcp", addr); err == nil {
			go srv.Serve(l)
			listening = true
		}
	}
	if !listening {
		fmt.Printf("Cannot listen on %s: after signing in, paste the address of the page that fails to load.\n", redirect.Host)
	}

	// A sign-in on another device ends on its own 127.0.0.1, which fails:
	// its address can be pasted here instead.
	go func() {
		scan := bufio.NewScanner(os.Stdin)
		for scan.Scan() {
			fmt.Fprintln(stdin, scan.Text())
		}
	}()

	scan := bufio.NewScanner(stdout)
	scan.Buffer(nil, 64<<10)
	for scan.Scan() {
		line := scan.Text()
		if link, ok := strings.CutPrefix(line, "open "); ok {
			fmt.Println("Opening " + name + "'s sign-in in your browser. If it does not open, visit:\n\n" + link + "\n")
			openBrowser(link)
			continue
		}
		fmt.Println(line)
	}
	stdin.Close()
	return quiet(cmd.Wait())
}
