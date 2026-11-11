// Package mail lets the agent use the user's mailbox with an app password
// it cannot read.
//
// den serves IMAP and SMTP on the container's loopback address. Any client
// logs in with any name and password: den logs in to the real server with
// the user's account and then passes the IMAP session through. Mail sent
// through den's SMTP waits until the user allows it in Telegram, and only
// then goes to the real server.
package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
)

const (
	// File is the secret with the account.
	File = "mail"

	// IMAP and SMTP are where den serves the agent.
	IMAP = "127.0.0.1:1143"
	SMTP = "127.0.0.1:1025"
)

// Account is the user's mailbox.
type Account struct {
	Address  string
	Password string
	// IMAP and SMTP are the servers as host:port. IMAP uses TLS. SMTP on
	// port 465 uses TLS and on other ports STARTTLS.
	IMAP string
	SMTP string
}

// providers are the servers of common mail domains.
var providers = map[string][2]string{
	"gmail.com":      {"imap.gmail.com:993", "smtp.gmail.com:465"},
	"googlemail.com": {"imap.gmail.com:993", "smtp.gmail.com:465"},
	"icloud.com":     {"imap.mail.me.com:993", "smtp.mail.me.com:587"},
	"me.com":         {"imap.mail.me.com:993", "smtp.mail.me.com:587"},
	"mac.com":        {"imap.mail.me.com:993", "smtp.mail.me.com:587"},
	"fastmail.com":   {"imap.fastmail.com:993", "smtp.fastmail.com:465"},
	"yahoo.com":      {"imap.mail.yahoo.com:993", "smtp.mail.yahoo.com:465"},
	"aol.com":        {"imap.aol.com:993", "smtp.aol.com:465"},
	"zoho.com":       {"imap.zoho.com:993", "smtp.zoho.com:465"},
	"gmx.com":        {"imap.gmx.com:993", "mail.gmx.com:465"},
}

// ErrAccount means the user has not added the account yet.
// It points at the google skill too, since Gmail connected with Google
// sign-in goes through the Google APIs rather than IMAP and SMTP.
var ErrAccount = errors.New("den has no IMAP and SMTP account. If the user connected Google, use the google skill for Gmail instead; otherwise they run den connect mail")

// Load reads the account: lines of address=, password=, and, for a domain
// den does not know, imap= and smtp=.
func Load(cfg config.Config) (Account, error) {
	data, err := os.ReadFile(filepath.Join(cfg.Secrets, File))
	if err != nil {
		return Account{}, ErrAccount
	}

	var a Account
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "address":
			a.Address = value
		case "password":
			a.Password = value
		case "imap":
			a.IMAP = value
		case "smtp":
			a.SMTP = value
		}
	}
	if a.Address == "" || a.Password == "" {
		return Account{}, fmt.Errorf("the secret %s needs address= and password=", File)
	}

	_, domain, _ := strings.Cut(strings.ToLower(a.Address), "@")
	if known, ok := providers[domain]; ok {
		if a.IMAP == "" {
			a.IMAP = known[0]
		}
		if a.SMTP == "" {
			a.SMTP = known[1]
		}
	}
	if a.IMAP == "" || a.SMTP == "" {
		return Account{}, fmt.Errorf("den does not know the servers of %s: add imap=host:993 and smtp=host:465 to the secret %s", domain, File)
	}
	return a, nil
}

// Ask asks the user to allow an action and returns nil when they do.
type Ask func(ctx context.Context, q approve.Question) error

type Server struct {
	cfg config.Config
	ask Ask
	// dial reaches the account's IMAP server.
	dial func(addr string) (net.Conn, error)
	// send hands a message to the account's SMTP server.
	send func(a Account, to []string, msg []byte) error
}

func New(cfg config.Config, ask Ask) *Server {
	return &Server{
		cfg: cfg,
		ask: ask,
		dial: func(addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			return tls.DialWithDialer(&net.Dialer{Timeout: 30 * time.Second}, "tcp", addr, &tls.Config{ServerName: host})
		},
		send: send,
	}
}

// Serve serves IMAP and SMTP until ctx ends.
func (s *Server) Serve(ctx context.Context) error {
	imap, err := net.Listen("tcp", IMAP)
	if err != nil {
		return err
	}
	smtp, err := net.Listen("tcp", SMTP)
	if err != nil {
		imap.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		imap.Close()
		smtp.Close()
	}()

	go accept(imap, func(c net.Conn) { s.serveIMAP(ctx, c) })
	accept(smtp, func(c net.Conn) { s.serveSMTP(ctx, c) })
	return nil
}

func accept(l net.Listener, serve func(net.Conn)) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go serve(c)
	}
}

// serveIMAP answers the client until it logs in, then logs in to the real
// server and passes the session through.
func (s *Server) serveIMAP(ctx context.Context, c net.Conn) {
	defer c.Close()

	a, err := Load(s.cfg)
	if err != nil {
		fmt.Fprintf(c, "* BYE %s\r\n", err)
		return
	}
	fmt.Fprint(c, "* OK den IMAP ready\r\n")

	r := bufio.NewReader(c)
	for {
		c.SetReadDeadline(time.Now().Add(5 * time.Minute))
		line, err := command(r, c)
		if err != nil {
			return
		}
		tag, rest, _ := strings.Cut(line, " ")
		name, _, _ := strings.Cut(rest, " ")

		switch strings.ToUpper(name) {
		case "CAPABILITY":
			fmt.Fprintf(c, "* CAPABILITY IMAP4rev1\r\n%s OK CAPABILITY completed\r\n", tag)
		case "NOOP":
			fmt.Fprintf(c, "%s OK NOOP completed\r\n", tag)
		case "LOGOUT":
			fmt.Fprintf(c, "* BYE\r\n%s OK LOGOUT completed\r\n", tag)
			return
		case "LOGIN":
			up, err := s.login(a)
			if err != nil {
				log.Printf("mail: IMAP login to %s: %v", a.IMAP, err)
				fmt.Fprintf(c, "%s NO %s\r\n", tag, oneLine(err))
				continue
			}
			defer up.Close()
			c.SetReadDeadline(time.Time{})
			fmt.Fprintf(c, "%s OK LOGIN completed\r\n", tag)
			pipe(c, r, up)
			return
		default:
			fmt.Fprintf(c, "%s BAD log in with LOGIN first\r\n", tag)
		}
	}
}

// upstream is the real IMAP server after den logged in.
type upstream struct {
	net.Conn
	r *bufio.Reader
}

func (s *Server) login(a Account) (*upstream, error) {
	conn, err := s.dial(a.IMAP)
	if err != nil {
		return nil, err
	}
	up := &upstream{Conn: conn, r: bufio.NewReader(conn)}
	conn.SetDeadline(time.Now().Add(time.Minute))

	greeting, err := up.r.ReadString('\n')
	if err != nil || !strings.HasPrefix(greeting, "* OK") {
		conn.Close()
		return nil, fmt.Errorf("the server greeted with %q", strings.TrimSpace(greeting))
	}

	fmt.Fprintf(conn, "den1 LOGIN %s %s\r\n", quote(a.Address), quote(a.Password))
	for {
		line, err := up.r.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, err
		}
		if strings.HasPrefix(line, "den1 ") {
			if !strings.HasPrefix(line, "den1 OK") {
				conn.Close()
				return nil, fmt.Errorf("the server refused the login: %s", strings.TrimSpace(strings.TrimPrefix(line, "den1 ")))
			}
			break
		}
	}
	conn.SetDeadline(time.Time{})
	return up, nil
}

// pipe passes bytes both ways until either side closes.
func pipe(c net.Conn, r *bufio.Reader, up *upstream) {
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(up, r)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(c, up.r)
		done <- struct{}{}
	}()
	<-done
}

// command reads one IMAP command line, taking in the literals it carries.
func command(r *bufio.Reader, w io.Writer) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		if b.Len()+len(line) > 64<<10 {
			return "", errors.New("command too long")
		}
		b.WriteString(line)

		trimmed := strings.TrimRight(line, "\r\n")
		open := strings.LastIndex(trimmed, "{")
		if open < 0 || !strings.HasSuffix(trimmed, "}") {
			return strings.TrimRight(b.String(), "\r\n"), nil
		}
		size := strings.TrimSuffix(trimmed[open+1:len(trimmed)-1], "+")
		var n int
		if _, err := fmt.Sscan(size, &n); err != nil || n < 0 || n > 64<<10 {
			return strings.TrimRight(b.String(), "\r\n"), nil
		}
		if !strings.HasSuffix(trimmed, "+}") {
			fmt.Fprint(w, "+ go\r\n")
		}
		literal := make([]byte, n)
		if _, err := io.ReadFull(r, literal); err != nil {
			return "", err
		}
		b.Write(literal)
	}
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func oneLine(err error) string {
	return strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", " "), "\n", " ")
}

// Check logs in to the account's IMAP server, to tell the user at once when
// the address or password is wrong.
func Check(a Account) error {
	s := New(config.Config{}, nil)
	up, err := s.login(a)
	if err != nil {
		return err
	}
	fmt.Fprint(up, "den2 LOGOUT\r\n")
	return up.Close()
}

// Parse reads an account in the secret's format, filling in the servers of
// a domain den knows.
func Parse(text string) (Account, error) {
	dir, err := os.MkdirTemp("", "den-mail")
	if err != nil {
		return Account{}, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, File), []byte(text), 0o600); err != nil {
		return Account{}, err
	}
	return Load(config.Config{Secrets: dir})
}
