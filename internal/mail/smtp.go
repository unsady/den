package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/letter"
)

// maxMessage bounds a message the agent sends.
const maxMessage = 25 << 20

// serveSMTP takes messages from the agent and sends each after the user
// allows it. Any login is accepted: the client is already inside den's
// container.
func (s *Server) serveSMTP(ctx context.Context, c net.Conn) {
	defer c.Close()

	r := bufio.NewReader(c)
	reply := func(format string, args ...any) {
		fmt.Fprintf(c, format+"\r\n", args...)
	}
	read := func() (string, error) {
		c.SetReadDeadline(time.Now().Add(5 * time.Minute))
		line, err := r.ReadString('\n')
		if len(line) > 4096 {
			return "", errors.New("line too long")
		}
		return strings.TrimRight(line, "\r\n"), err
	}

	reply("220 den ESMTP ready")
	var to []string
	for {
		line, err := read()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")

		switch strings.ToUpper(verb) {
		case "EHLO":
			reply("250-den\r\n250-AUTH PLAIN LOGIN\r\n250-8BITMIME\r\n250 SIZE %d", maxMessage)
		case "HELO":
			reply("250 den")
		case "AUTH":
			mechanism, initial, _ := strings.Cut(arg, " ")
			switch strings.ToUpper(mechanism) {
			case "PLAIN":
				if initial == "" {
					reply("334 ")
					read()
				}
			case "LOGIN":
				reply("334 VXNlcm5hbWU6")
				read()
				reply("334 UGFzc3dvcmQ6")
				read()
			}
			reply("235 accepted: den uses the user's account")
		case "MAIL":
			to = nil
			reply("250 OK")
		case "RCPT":
			address := recipient(arg)
			if len(to) >= 100 || address == "" {
				reply("452 too many recipients")
				continue
			}
			to = append(to, address)
			reply("250 OK")
		case "DATA":
			if len(to) == 0 {
				reply("503 RCPT first")
				continue
			}
			reply("354 end with <CRLF>.<CRLF>")
			c.SetReadDeadline(time.Now().Add(5 * time.Minute))
			msg, err := data(r)
			if err != nil {
				reply("552 %s", oneLine(err))
				return
			}
			c.SetReadDeadline(time.Time{})
			reply("%s", s.deliver(ctx, to, msg))
			to = nil
		case "RSET":
			to = nil
			reply("250 OK")
		case "NOOP":
			reply("250 OK")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not supported")
		}
	}
}

// deliver sends a message after the user allows it and returns the SMTP
// reply for the client.
func (s *Server) deliver(ctx context.Context, to []string, msg []byte) string {
	a, err := Load(s.cfg)
	if err != nil {
		return "451 " + oneLine(err)
	}

	text := "✉︎ Email"
	if shown := letter.Show(msg, to); shown != "" {
		text += "\n\n" + shown
	} else {
		text += " to " + strings.Join(to, ", ")
	}
	// Each mail is new, so it is asked about every time. The client does
	// not wait for the answer: den sends the message once the user allows
	// it, and tells the agent.
	turn := ""
	if s.Turn != nil {
		turn = s.Turn()
	}
	what := "Mail to " + strings.Join(to, ", ")
	_, err = s.later(approve.Question{Text: text, What: what}, func(answer error) {
		s.sendLater(a, to, msg, what, turn, answer)
	})
	if err != nil {
		log.Printf("mail: to %s: not sent: %v", strings.Join(to, ", "), err)
		return "554 not sent: " + oneLine(err)
	}
	return "250 waiting for the user: den sends it once they allow it in Telegram, and tells you"
}

// sendLater sends a message the user answered, and reports what came of it.
func (s *Server) sendLater(a Account, to []string, msg []byte, what, turn string, answer error) {
	report := func(agent, user string) {
		if s.Report != nil {
			s.Report(turn, agent, user)
		}
	}
	if answer != nil {
		log.Printf("mail: to %s: not sent: %v", strings.Join(to, ", "), answer)
		if !errors.Is(answer, context.Canceled) {
			report(fmt.Sprintf("[den: %s was not sent: %v.]", what, answer), "")
		}
		return
	}
	if err := s.send(a, to, msg); err != nil {
		log.Printf("mail: to %s: %v", strings.Join(to, ", "), err)
		report(fmt.Sprintf("[den: the user allowed %s, and sending it failed: %v.]", what, err), fmt.Sprintf("%s failed: %v", what, err))
		return
	}
	log.Printf("mail: sent to %s, allowed by the user", strings.Join(to, ", "))
	report(fmt.Sprintf("[den: the user allowed %s, and den sent it.]", what), "")
}

// recipient takes the address from RCPT's argument, "TO:<a@b.c>".
func recipient(arg string) string {
	if open := strings.Index(arg, "<"); open >= 0 {
		if end := strings.Index(arg[open:], ">"); end > 0 {
			return strings.TrimSpace(arg[open+1 : open+end])
		}
	}
	_, address, _ := strings.Cut(arg, ":")
	address, _, _ = strings.Cut(strings.TrimSpace(address), " ")
	return address
}

// data reads a message after DATA up to the line with a single dot.
func data(r *bufio.Reader) ([]byte, error) {
	var b bytes.Buffer
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if strings.TrimRight(line, "\r\n") == "." {
			return b.Bytes(), nil
		}
		line = strings.TrimPrefix(line, ".")
		if b.Len()+len(line) > maxMessage {
			io.Copy(io.Discard, io.LimitReader(r, maxMessage))
			return nil, fmt.Errorf("the message is over %d MB", maxMessage>>20)
		}
		b.WriteString(line)
	}
}

// send hands a message to the account's SMTP server.
func send(a Account, to []string, msg []byte) error {
	host, port, err := net.SplitHostPort(a.SMTP)
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second}

	var conn net.Conn
	if port == "465" {
		conn, err = tls.DialWithDialer(dialer, "tcp", a.SMTP, &tls.Config{ServerName: host})
	} else {
		conn, err = dialer.Dial("tcp", a.SMTP)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(5 * time.Minute))

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if port != "465" {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if err := c.Auth(smtp.PlainAuth("", a.Address, a.Password, host)); err != nil {
		return err
	}
	if err := c.Mail(a.Address); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}

	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
