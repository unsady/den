package mail

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
)

func setup(t *testing.T, secret string) config.Config {
	t.Helper()

	cfg := config.Config{Secrets: t.TempDir()}
	if secret != "" {
		os.WriteFile(filepath.Join(cfg.Secrets, File), []byte(secret), 0o600)
	}
	return cfg
}

func TestLoad(t *testing.T) {
	a, err := Load(setup(t, "address=me@gmail.com\npassword=abcd efgh ijkl mnop\n"))
	if err != nil || a.IMAP != "imap.gmail.com:993" || a.SMTP != "smtp.gmail.com:465" || a.Password != "abcd efgh ijkl mnop" {
		t.Fatalf("gmail: %+v %v", a, err)
	}

	if _, err := Load(setup(t, "address=me@example.org\npassword=x\n")); err == nil || !strings.Contains(err.Error(), "imap=") {
		t.Fatalf("unknown domain: %v", err)
	}
	if a, err := Load(setup(t, "address=me@example.org\npassword=x\nimap=mail.example.org:993\nsmtp=mail.example.org:587\n")); err != nil || a.SMTP != "mail.example.org:587" {
		t.Fatalf("own servers: %+v %v", a, err)
	}
	if _, err := Load(setup(t, "")); !errors.Is(err, ErrAccount) {
		t.Fatalf("no secret: %v", err)
	}
}

// serve runs handle for each connection to a new loopback listener.
func serve(t *testing.T, handle func(net.Conn)) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go accept(l, handle)
	return l.Addr().String()
}

// fakeIMAP accepts the account's password and answers SELECT.
func fakeIMAP(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	fmt.Fprint(c, "* OK fake ready\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch {
		case line == "den1 LOGIN \"me@gmail.com\" \"pw\"\r\n":
			fmt.Fprint(c, "* CAPABILITY IMAP4rev1 IDLE\r\nden1 OK logged in\r\n")
		case strings.HasPrefix(line, "den1 LOGIN"):
			fmt.Fprint(c, "den1 NO [AUTHENTICATIONFAILED] Invalid credentials\r\n")
		case strings.HasPrefix(line, "a2 SELECT"):
			fmt.Fprint(c, "* 3 EXISTS\r\na2 OK selected\r\n")
		}
	}
}

func imapSession(t *testing.T, secret string) (*bufio.Reader, net.Conn) {
	t.Helper()

	upstream := serve(t, fakeIMAP)
	s := New(setup(t, secret), nil)
	s.dial = func(string) (net.Conn, error) { return net.Dial("tcp", upstream) }
	proxy := serve(t, func(c net.Conn) { s.serveIMAP(context.Background(), c) })

	c, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	r := bufio.NewReader(c)
	if greeting, _ := r.ReadString('\n'); !strings.HasPrefix(greeting, "* OK") {
		t.Fatalf("greeting: %q", greeting)
	}
	return r, c
}

func TestIMAPPassesTheSessionThrough(t *testing.T) {
	r, c := imapSession(t, "address=me@gmail.com\npassword=pw\nsmtp=x:465\n")

	fmt.Fprint(c, "a0 CAPABILITY\r\n")
	r.ReadString('\n')
	if line, _ := r.ReadString('\n'); !strings.HasPrefix(line, "a0 OK") {
		t.Fatalf("capability: %q", line)
	}

	// The client's own name and password do not matter, even as literals.
	fmt.Fprint(c, "a1 LOGIN {5}\r\n")
	if line, _ := r.ReadString('\n'); !strings.HasPrefix(line, "+") {
		t.Fatalf("literal: %q", line)
	}
	fmt.Fprint(c, "agent anything\r\n")
	if line, _ := r.ReadString('\n'); line != "a1 OK LOGIN completed\r\n" {
		t.Fatalf("login: %q", line)
	}

	fmt.Fprint(c, "a2 SELECT INBOX\r\n")
	if line, _ := r.ReadString('\n'); line != "* 3 EXISTS\r\n" {
		t.Fatalf("select: %q", line)
	}
	if line, _ := r.ReadString('\n'); line != "a2 OK selected\r\n" {
		t.Fatalf("select: %q", line)
	}
}

func TestIMAPReportsARefusedLogin(t *testing.T) {
	r, c := imapSession(t, "address=me@gmail.com\npassword=wrong\n")

	fmt.Fprint(c, "a1 LOGIN x y\r\n")
	if line, _ := r.ReadString('\n'); !strings.HasPrefix(line, "a1 NO") || !strings.Contains(line, "Invalid credentials") {
		t.Fatalf("login: %q", line)
	}
}

func TestSMTPSendsWhatTheUserAllows(t *testing.T) {
	var asked []string
	allow := true
	s := New(setup(t, "address=me@gmail.com\npassword=pw\n"), func(q approve.Question, then func(error)) (bool, error) {
		asked = append(asked, q.Text)
		if !allow {
			then(errors.New("denied"))
		} else {
			then(nil)
		}
		return true, nil
	})
	var reports []string
	s.Turn = func() string { return "t1" }
	s.Report = func(turn, agent, user string) { reports = append(reports, turn+" "+agent) }
	var sent []string
	s.send = func(a Account, to []string, msg []byte) error {
		sent = append(sent, a.SMTP+" "+strings.Join(to, ",")+" "+string(msg))
		return nil
	}
	addr := serve(t, func(c net.Conn) { s.serveSMTP(context.Background(), c) })

	msg := []byte("To: Anya <anya@example.com>\r\nSubject: Dinner\r\n\r\nAt 7?\r\n.hidden dot\r\n")
	if err := smtp.SendMail(addr, nil, "agent@den", []string{"anya@example.com", "boss@example.com"}, msg); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "Subject: Dinner") || !strings.Contains(asked[0], "Bcc: boss@example.com") {
		t.Fatalf("asked: %q", asked)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "smtp.gmail.com:465 anya@example.com,boss@example.com") || !strings.Contains(sent[0], "\r\n.hidden dot") {
		t.Fatalf("sent: %q", sent)
	}

	allow = false
	err := smtp.SendMail(addr, nil, "agent@den", []string{"anya@example.com"}, msg)
	if err != nil || len(sent) != 1 {
		t.Fatalf("denied: %v, sent %d", err, len(sent))
	}
	want := "t1 [den: the user allowed Mail to anya@example.com, boss@example.com, and den sent it.]|t1 [den: Mail to anya@example.com was not sent: denied.]"
	if got := strings.Join(reports, "|"); got != want {
		t.Fatalf("reports %q", got)
	}
}

func TestRecipient(t *testing.T) {
	for arg, want := range map[string]string{
		"TO:<a@b.c>":               "a@b.c",
		"To: <a@b.c> NOTIFY=NEVER": "a@b.c",
		"TO:a@b.c":                 "a@b.c",
	} {
		if got := recipient(arg); got != want {
			t.Errorf("%q: %q", arg, got)
		}
	}
}

func TestFilterTurnsAwayDeletes(t *testing.T) {
	in := "a1 UID STORE 5 +FLAGS (\\Seen)\r\n" +
		"a2 APPEND Drafts {21}\r\nSubject: EXPUNGE me\r\n\r\n" +
		"a3 uid store 5 +flags (\\Deleted)\r\n" +
		"a4 EXPUNGE\r\n" +
		"a5 UID MOVE 5 Trash\r\n"
	var client, up strings.Builder
	filter(&client, bufio.NewReader(strings.NewReader(in)), &up)

	want := "a1 UID STORE 5 +FLAGS (\\Seen)\r\na2 APPEND Drafts {21}\r\nSubject: EXPUNGE me\r\n\r\na5 UID MOVE 5 Trash\r\n"
	if up.String() != want {
		t.Fatalf("passed on %q", up.String())
	}
	if got := client.String(); !strings.HasPrefix(got, "a3 NO den: ") || !strings.Contains(got, "\r\na4 NO den: ") {
		t.Fatalf("answered %q", got)
	}
}
