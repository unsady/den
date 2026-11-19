package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
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
  den update                    Update the den command and its image, and recreate the
                                container; data stays
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
  den connect grok              Connect a Grok subscription for Grok models
  den connect claude            Connect a Claude Pro or Max subscription
  den connect mail              Connect a mailbox with an app password
  den disconnect google | codex | grok | claude | mail
  den secret set <name> | ls | rm <name>
                                Keep a secret, such as github_token; asks for it
  den service add [--header <name>] [--ask writes|never|always] [--about <text>] <name> <https-url>
                                Let the agent call an API with your token, which
                                it never sees; asks for the token
  den service ls | rm <name>
  den app pair [--address <ip>] Let the den app connect, on this machine's Tailscale
                                address by default; prints what to paste in the app
  den app unpair                Shut every app out

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
	// App is the address of this machine where the app reaches den, set by
	// den app pair; empty keeps den's port closed.
	App string `json:"app,omitempty"`
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
		return launch(c, "Starting den", start)
	case "stop":
		if err := step("Stopping den", func(context.Context) error { return c.quiet("stop", c.Name) }); err != nil {
			return err
		}
		done("den stopped")
		return nil
	case "restart":
		return launch(c, "Restarting den", recreate)
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
	case "app":
		return hostApp(c, args[1:])
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
	// Only the commands that handle secrets run as root. The rest run as
	// the agent, so they do not act as root on files the agent controls,
	// such as the sessions they export.
	if rootCommands[args[0]] {
		return inside(c, "", args...)
	}
	return agentExec(c, append([]string{"den"}, args...)...)
}

// rootCommands are the commands that need den's secrets.
var rootCommands = map[string]bool{
	"secret": true, "secrets": true, "service": true, "services": true,
	"login": true, "connect": true, "disconnect": true, "serve": true,
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

// insideQuiet runs den with args in the container, with input on its
// stdin, and shows its output only when it fails.
func insideQuiet(c hostConfig, input string, args ...string) error {
	cmd := c.engine().command(append([]string{"exec", "-i", c.Name, "den"}, args...)...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "den: ")))
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
		if err := c.quiet("volume", "create", c.Volume); err != nil {
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
	return nil
}

// launch starts den with how, under a spinner titled title, and says
// where it runs.
func launch(c hostConfig, title string, how func(hostConfig) error) error {
	if err := step(title, func(context.Context) error { return how(c) }); err != nil {
		return err
	}
	done("den is running in the container %s", c.Name)
	return nil
}

// quiet runs the container tool with args and shows its output only when
// it fails.
func (c hostConfig) quiet(args ...string) error {
	out, err := c.engine().command(args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v\n%s", c.engine(), args[0], err, lastLines(string(out), 20))
	}
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
	if err := updateCommand(c); err != nil {
		return err
	}
	old := keepPrevious(c)
	if err := pullOrBuild(c); err != nil {
		return err
	}

	if err := launch(c, "Restarting den", recreate); err != nil {
		return err
	}
	if old != "" {
		c.engine().removeImage(old).Run()
	}
	return nil
}

// keepPrevious tags the image den runs now with its previous tag and
// returns that tag, or "" when there is no image yet. After a new build
// or pull takes the image's own tag, the old image would otherwise stay
// on disk with no name to remove it by: Apple's container keeps a whole
// unpacked copy of every image.
func keepPrevious(c hostConfig) string {
	old := previous(c.Image)
	if old == "" || !c.exists("image", "inspect", c.Image) {
		return ""
	}

	if c.engine().command("image", "tag", c.Image, old).Run() != nil {
		return ""
	}
	return old
}

// pullOrBuild gets den's image under a spinner.
func pullOrBuild(c hostConfig) error {
	title := "Pulling " + c.Image
	if c.Source != "" {
		title = "Building the image from " + c.Source
	}
	if err := step(title, func(context.Context) error { return getImage(c) }); err != nil {
		return err
	}
	done("Image %s", c.Image)
	return nil
}

// getImage builds the image from the den checkout, or pulls it without
// one.
func getImage(c hostConfig) error {
	e := c.engine()
	if c.Source == "" {
		pull := []string{"pull", c.Image}
		if e == appleContainer {
			pull = []string{"image", "pull", c.Image}
		}
		if out, err := e.command(pull...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s pull: %v\n%s", e, err, lastLines(string(out), 20))
		}
		return nil
	}

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
