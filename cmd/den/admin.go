package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/unsady/den/internal/client"
	"github.com/unsady/den/internal/codex"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/fetch"
	"github.com/unsady/den/internal/google"
	"github.com/unsady/den/internal/mail"
	"github.com/unsady/den/internal/models"
	"github.com/unsady/den/internal/runner"
)

// These commands change den's secrets, so they run as root inside the
// container. On the host, den runs them there and reads secrets from the
// terminal, never from the command line.

// secretNames are the files a secret may be written to.
var secretNames = regexp.MustCompile(`^[a-z0-9_]+$`)

func admin() error {
	if os.Geteuid() != 0 {
		return errors.New("only den itself changes its secrets: run this with den on the host, not as the agent")
	}
	return nil
}

func secretCommand(cfg config.Config, args []string) error {
	if err := admin(); err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("secret needs a subcommand: set, ls, or rm")
	}

	switch args[0] {
	case "set":
		name, err := oneArg("secret set", args[1:])
		if err != nil {
			return err
		}
		if !secretNames.MatchString(name) {
			return fmt.Errorf("%q is not a secret name: use lowercase letters, digits, and _", name)
		}
		value, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(value)) == "" {
			return errors.New("the secret is empty: pass it on stdin")
		}
		return writeSecret(cfg, name, strings.TrimRight(string(value), "\r\n")+"\n")

	case "ls", "list":
		entries, err := os.ReadDir(cfg.Secrets)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".tmp") {
				fmt.Println(e.Name())
			}
		}
		return nil

	case "rm", "remove":
		name, err := oneArg("secret rm", args[1:])
		if err != nil {
			return err
		}
		if !secretNames.MatchString(name) {
			return fmt.Errorf("%q is not a secret name", name)
		}
		return os.Remove(filepath.Join(cfg.Secrets, name))
	}
	return fmt.Errorf("unknown secret subcommand %q", args[0])
}

func writeSecret(cfg config.Config, name, value string) error {
	if err := os.MkdirAll(cfg.Secrets, 0o700); err != nil {
		return err
	}
	path := filepath.Join(cfg.Secrets, name)
	if err := os.WriteFile(path+".tmp", []byte(value), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func loginCommand(cfg config.Config, args []string) error {
	if err := admin(); err != nil {
		return err
	}
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	model := fs.String("model", "", "the model to use by default")
	baseURL := fs.String("base-url", "", "the provider's address, when models.dev has none")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("login takes one provider id, such as opencode-go, and reads the key from stdin")
	}

	key, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
	if err != nil {
		return err
	}
	if err := models.New(cfg).Login(rest[0], *baseURL, string(key), *model); err != nil {
		return err
	}
	fmt.Printf("signed in to %s\n", rest[0])
	return nil
}

func connectCommand(cfg config.Config, args []string) error {
	if err := admin(); err != nil {
		return err
	}
	service, err := oneArg("connect", args)
	if err != nil {
		return err
	}

	switch service {
	case "google":
		return connectGoogle(cfg)
	case "codex":
		c := codex.New(cfg)
		s := c.Begin()
		if err := relay(s.URL, func() (string, error) { return c.Finish(context.Background(), s) }, c.Deliver, codex.ErrNotSignin); err != nil {
			return err
		}
		// fx refuses a provider without a model of its own.
		return models.New(cfg).Default("codex", func() ([]string, error) { return runner.Offered(context.Background(), cfg, "codex") })
	case "mail":
		text, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return err
		}
		a, err := mail.Parse(string(text))
		if err != nil {
			return err
		}
		if err := mail.Check(a); err != nil {
			return fmt.Errorf("%s: %v", a.IMAP, err)
		}
		if err := writeSecret(cfg, mail.File, string(text)); err != nil {
			return err
		}
		fmt.Printf("connected %s\n", a.Address)
		return nil
	}
	return fmt.Errorf("den connects google, codex, or mail, not %q", service)
}

// connectGoogle signs in over stdio: it prints the sign-in address as
// "open <url>", reads the address the browser came back to, and prints the
// account. den on the host opens the browser and catches the address.
func connectGoogle(cfg config.Config) error {
	g := google.New(cfg)
	s, err := g.Begin()
	if errors.Is(err, google.ErrClient) {
		return fmt.Errorf("the Google client is missing: run den connect google <client JSON> on the host")
	}
	if err != nil {
		return err
	}
	return relay(s.URL, func() (string, error) { return g.Finish(context.Background(), s) }, g.Deliver, google.ErrNotSignin)
}

// relay signs in over stdio: it prints the sign-in address as "open <url>",
// hands each line it reads to deliver until finish returns, and prints the
// account.
func relay(link string, finish func() (string, error), deliver func(string) error, notSignin error) error {
	type result struct {
		email string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		email, err := finish()
		done <- result{email, err}
	}()

	fmt.Printf("open %s\n", link)
	lines := make(chan string)
	go func() {
		scan := bufio.NewScanner(os.Stdin)
		scan.Buffer(nil, 64<<10)
		for scan.Scan() {
			lines <- scan.Text()
		}
		close(lines)
	}()

	for {
		select {
		case r := <-done:
			if r.err != nil {
				return r.err
			}
			fmt.Printf("connected %s\n", r.email)
			return nil
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			// Finish may not wait for the address yet in its first moment.
			for try := 0; ; try++ {
				err := deliver(line)
				if err == nil || errors.Is(err, notSignin) || try == 20 {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
}

func disconnectCommand(cfg config.Config, args []string) error {
	if err := admin(); err != nil {
		return err
	}
	service, err := oneArg("disconnect", args)
	if err != nil {
		return err
	}
	switch service {
	case "google":
		return google.New(cfg).Disconnect(context.Background())
	case "codex":
		return codex.New(cfg).Disconnect()
	case "mail":
		return os.Remove(filepath.Join(cfg.Secrets, mail.File))
	}
	return fmt.Errorf("den disconnects google, codex, or mail, not %q", service)
}

// serviceCommand adds, lists, and removes the APIs den sends a token to.
// Listing works for the agent too, through the daemon, without the tokens.
func serviceCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("service needs a subcommand: add, ls, or rm")
	}
	switch args[0] {
	case "ls", "list":
		var list []fetch.Custom
		if err := client.New(cfg).Do(http.MethodGet, "/services", nil, &list); err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("No services. The user adds one with den service add on their computer.")
		}
		for _, s := range list {
			line := fmt.Sprintf("%s  https://%s  %s", s.Name, s.Host, askText(s.Ask))
			if s.About != "" {
				line += "\n    " + strings.ReplaceAll(s.About, "\n", "\n    ")
			}
			fmt.Println(line)
		}
		return nil

	case "add":
		if err := admin(); err != nil {
			return err
		}
		fs := flag.NewFlagSet("service add", flag.ContinueOnError)
		header := fs.String("header", "", "send the token as it is in this header, instead of Authorization: Bearer")
		ask := fs.String("ask", fetch.AskWrites, "which requests wait for the user: writes, never, or always")
		about := fs.String("about", "", "what the API does and where its docs are, for the agent")
		rest, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 2 {
			return errors.New("service add takes a name and the API's https address, and reads the token from stdin")
		}
		s, err := fetch.NewCustom(rest[0], rest[1], *header, *ask, *about)
		if err != nil {
			return err
		}
		token, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(token)) == "" {
			return errors.New("the token is empty: pass it on stdin")
		}
		list, err := fetch.LoadCustom(cfg)
		if err != nil {
			return err
		}
		list = slices.DeleteFunc(list, func(c fetch.Custom) bool { return c.Name == s.Name })
		if err := writeSecret(cfg, s.SecretFile(), strings.TrimSpace(string(token))+"\n"); err != nil {
			return err
		}
		if err := fetch.SaveCustom(cfg, append(list, s)); err != nil {
			return err
		}
		fmt.Printf("added %s: den sends its token to https://%s and %s\n", s.Name, s.Host, askText(s.Ask))
		return nil

	case "rm", "remove":
		if err := admin(); err != nil {
			return err
		}
		if len(args) != 2 {
			return errors.New("service rm takes a service name")
		}
		list, err := fetch.LoadCustom(cfg)
		if err != nil {
			return err
		}
		kept := slices.DeleteFunc(slices.Clone(list), func(c fetch.Custom) bool { return c.Name == args[1] })
		if len(kept) == len(list) {
			return fmt.Errorf("there is no service %q", args[1])
		}
		if err := fetch.SaveCustom(cfg, kept); err != nil {
			return err
		}
		os.Remove(filepath.Join(cfg.Secrets, args[1]+"_token"))
		return nil
	}
	return fmt.Errorf("unknown service subcommand %q", args[0])
}

func askText(ask string) string {
	switch ask {
	case fetch.AskNever:
		return "asks the user about nothing"
	case fetch.AskAlways:
		return "asks the user before every request"
	}
	return "asks the user before requests that write"
}
