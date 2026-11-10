package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/unsady/den/internal/codex"
	"github.com/unsady/den/internal/google"
	"github.com/unsady/den/internal/mail"
)

// On the host, a Mac or a Linux machine, den runs den in its container: it
// starts and updates the container, reads secrets from the terminal, and
// passes every other command to den inside. In the image, the same binary
// is den itself.

func init() {
	if !inContainer() {
		host = hostRun
	}
}

const hostUsage = `den on this machine runs den in a container.

Usage:
  den setup                     Set den up, or finish a setup: container, Telegram, model
  den update                    Rebuild the image and recreate the container; data stays
  den start | stop | restart
  den status                    The container, and which secrets den has
  den logs [-f]                 den's log
  den fx [<args>]               Run fx as the agent, such as den fx for a terminal chat
  den shell                     A shell as the agent
  den login [--model <id>] [--base-url <url>] <provider>
                                Add a model provider; asks for the key. --model
                                also makes it den's default
  den connect google [<client JSON>]
                                Connect Gmail and Calendar in this machine's browser
  den connect codex             Connect a ChatGPT subscription for Codex models
  den connect mail              Connect a mailbox with an app password
  den disconnect google | codex | mail
  den secret set <name> | ls | rm <name>
                                Keep a secret, such as github_token; asks for it
  den service add [--header <name>] [--ask writes|never|always] [--about <text>] <name> <https-url>
                                Let the agent call an API with your token, which
                                it never sees; asks for the token
  den service ls | rm <name>

Every other command, such as den job ls, runs in the container.
Settings: ~/.config/den/config.json
`

// hostConfig is how den runs its container.
type hostConfig struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	Volume string `json:"volume"`
	// Home is the agent's home on the host: its notes, sessions, and tools.
	Home   string            `json:"home"`
	CPUs   int               `json:"cpus"`
	Memory string            `json:"memory"`
	Env    map[string]string `json:"env,omitempty"`
	// Source is a den checkout to build the image from. Without one, den
	// pulls Image.
	Source string `json:"source,omitempty"`
	// Runtime is the tool that runs the container: container, Apple's, on
	// a Mac, or docker or podman. Empty picks the first one installed.
	Runtime string `json:"runtime,omitempty"`
}

func (c hostConfig) engine() engine { return pickEngine(c.Runtime) }

// run runs the container tool with args, in the terminal.
func (c hostConfig) run(args ...string) error {
	cmd := c.engine().command(args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return quiet(cmd.Run())
}

// exists reports whether the container tool's args succeed, such as
// inspect den.
func (c hostConfig) exists(args ...string) bool {
	return c.engine().command(args...).Run() == nil
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "den", "config.json")
}

func loadHost() (hostConfig, bool) {
	home, _ := os.UserHomeDir()
	c := hostConfig{
		Name:   "den",
		Image:  "den:latest",
		Volume: "den",
		Home:   filepath.Join(home, "den"),
		CPUs:   2,
		Memory: "1G",
		Env:    map[string]string{"TZ": localZone()},
	}
	data, err := os.ReadFile(configPath())
	if err != nil {
		return c, false
	}
	json.Unmarshal(data, &c)
	return c, true
}

func (c hostConfig) save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(configPath(), append(data, '\n'), 0o644)
}

// localZone names this machine's time zone, such as America/New_York.
func localZone() string {
	link, err := os.Readlink("/etc/localtime")
	if _, zone, ok := strings.Cut(link, "zoneinfo/"); err == nil && ok {
		return zone
	}
	return "UTC"
}

func hostRun(args []string) error {
	c, _ := loadHost()
	if len(args) == 0 {
		fmt.Print(hostUsage)
		return nil
	}
	// setup says how to install a missing tool; the rest need one.
	if _, err := exec.LookPath(string(c.engine())); err != nil && args[0] != "setup" && args[0] != "help" {
		return fmt.Errorf("den needs %s to run its container: install it, or run den setup", c.engine())
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(hostUsage)
		return nil
	case "setup":
		return setup(c)
	case "update":
		return update(c)
	case "start":
		return start(c)
	case "stop":
		return c.run("stop", c.Name)
	case "restart":
		return recreate(c)
	case "status":
		return status(c)
	case "logs":
		return c.run(append([]string{"logs"}, append(args[1:], c.Name)...)...)
	case "fx":
		return agentExec(c, append([]string{"fx"}, args[1:]...)...)
	case "shell":
		return agentExec(c, "bash", "-l")
	case "login":
		return hostLogin(c, args[1:])
	case "connect":
		return hostConnect(c, args[1:])
	case "service", "services":
		if len(args) > 1 && args[1] == "add" {
			token, err := ask("Token: ", true)
			if err != nil {
				return err
			}
			return inside(c, token, args...)
		}
	case "secret", "secrets":
		if len(args) == 3 && args[1] == "set" {
			value, err := ask("Value for "+args[2]+": ", true)
			if err != nil {
				return err
			}
			return inside(c, value, "secret", "set", args[2])
		}
	}
	return inside(c, "", args...)
}

// inside runs den with args in the container, with input on its stdin, or
// the terminal when input is "".
func inside(c hostConfig, input string, args ...string) error {
	flags := []string{"exec", "-i"}
	if input == "" && terminal() {
		flags = append(flags, "-t")
	}
	cmd := c.engine().command(append(append(flags, c.Name, "den"), args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	} else {
		cmd.Stdin = os.Stdin
	}
	if err := cmd.Run(); err != nil {
		return quiet(err)
	}
	return nil
}

func agentExec(c hostConfig, args ...string) error {
	flags := []string{"exec", "-i", "-u", "agent", "-w", "/home/agent/space", "-e", "HOME=/home/agent"}
	if terminal() {
		flags = append(flags, "-t")
	}
	cmd := c.engine().command(append(append(flags, c.Name), args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return quiet(cmd.Run())
}

// quiet exits with a command's own status, which already printed why.
func quiet(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	return err
}

func running(c hostConfig) bool {
	out, err := c.engine().command("inspect", c.Name).Output()
	return err == nil && strings.Contains(string(out), `"running"`)
}

func start(c hostConfig) error {
	if running(c) {
		return nil
	}
	e := c.engine()
	if c.exists("inspect", c.Name) {
		e.command("rm", c.Name).Run()
	}
	if !c.exists("volume", "inspect", c.Volume) {
		if err := c.run("volume", "create", c.Volume); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(c.Home, 0o755); err != nil {
		return err
	}

	if out, err := e.command(e.runArgs(c)...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s run: %v\n%s", e, err, out)
	}

	// den needs a moment to open its socket.
	for i := 0; i < 50 && !c.exists("exec", c.Name, "test", "-S", "/run/den/den.sock"); i++ {
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("den is running in the container %s\n", c.Name)
	return nil
}

func recreate(c hostConfig) error {
	if c.exists("inspect", c.Name) {
		c.engine().command("stop", c.Name).Run()
		c.engine().command("rm", c.Name).Run()
	}
	return start(c)
}

func update(c hostConfig) error {
	if err := getImage(c); err != nil {
		return err
	}
	return recreate(c)
}

// getImage builds the image from the den checkout, or pulls it without
// one.
func getImage(c hostConfig) error {
	e := c.engine()
	if c.Source == "" {
		fmt.Println("Pulling " + c.Image + "…")
		pull := []string{"pull", c.Image}
		if e == appleContainer {
			pull = []string{"image", "pull", c.Image}
		}
		if out, err := e.command(pull...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s pull: %v\n%s", e, err, lastLines(string(out), 20))
		}
		return nil
	}

	fmt.Println("Building the image…")
	out, err := e.command("build", "-t", c.Image, c.Source).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s build: %v\n%s", e, err, lastLines(string(out), 20))
	}
	return nil
}

func status(c hostConfig) error {
	if !running(c) {
		fmt.Printf("The container %s is not running: den start\n", c.Name)
		return nil
	}
	fmt.Printf("Container %s, image %s, %d CPUs, %s, home %s\n", c.Name, c.Image, c.CPUs, c.Memory, c.Home)
	out, err := c.engine().command("exec", c.Name, "den", "secret", "ls").Output()
	if err != nil {
		return err
	}
	fmt.Println("Secrets: " + strings.Join(strings.Fields(string(out)), ", "))
	return nil
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
	key, err := ask("API key for "+provider+": ", true)
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
	if err := inside(c, key, append(args, provider)...); err != nil {
		return err
	}
	if model == "" {
		fmt.Println("Its models are in /model in Telegram; the default model stays as it was.")
	}
	return nil
}

func hostConnect(c hostConfig, args []string) error {
	if len(args) == 0 {
		return errors.New("connect google, codex, or mail")
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
	}
	return fmt.Errorf("den connects google, codex, or mail, not %q", args[0])
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
	fmt.Println("Checking the login…")
	return inside(c, text, "connect", "mail")
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

// ask reads a line from the terminal, without echo when secret.
func ask(prompt string, secret bool) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if secret && terminal() {
		stty("-echo")
		defer func() {
			stty("echo")
			fmt.Fprintln(os.Stderr)
		}()
	}
	line, err := stdinReader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" && err != nil {
		return "", fmt.Errorf("nothing entered for %q", strings.TrimSuffix(prompt, ": "))
	}
	return line, nil
}

var stdinReader = bufio.NewReader(os.Stdin)

func stty(arg string) {
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	cmd.Run()
}

// terminal reports whether stdin is a terminal: a character device such as
// /dev/null is not.
func terminal() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), getTermios, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
