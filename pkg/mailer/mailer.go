// Package mailer sends plain-text emails, for example a password reset link.
//
// SMTP sends through an SMTP server (a Gmail account with an app password works). Log sends
// nothing and writes the message to the log instead: it is what a service uses on a developer's
// machine, where no mail server is configured.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Message is one plain-text email to one address.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Sender sends a message. Send returns after the mail server accepted or refused it, or when ctx
// is done, so give ctx a deadline.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTP sends through an SMTP server with STARTTLS, which is port 587 at most providers
// (smtp.gmail.com:587). A server that does not offer STARTTLS is refused, so the password and the
// message never travel unencrypted.
type SMTP struct {
	Host string
	Port int
	// Username and Password log in to the server. With an empty Username no login is tried.
	Username string
	Password string
	// From is the sender, either "noreply@example.com" or "Name <noreply@example.com>".
	From string
}

// Send delivers m to the SMTP server.
func (s SMTP) Send(ctx context.Context, m Message) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("mailer: sender address %q: %w", s.From, err)
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("mailer: recipient address: %w", err)
	}
	body := buildMessage(from, to, m, time.Now())

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return fmt.Errorf("mailer: connect: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer: greeting: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); !ok {
		return errors.New("mailer: the server does not offer STARTTLS")
	}
	if err := c.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("mailer: starttls: %w", err)
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("mailer: login: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("mailer: sender refused: %w", err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("mailer: recipient refused: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mailer: data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("mailer: write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: message refused: %w", err)
	}
	return c.Quit()
}

// buildMessage returns m as an RFC 5322 message: the headers, an empty line, the text, with CRLF
// line ends. A header value never holds a line break, so the text of m cannot add headers.
func buildMessage(from, to *mail.Address, m Message, now time.Time) []byte {
	var b strings.Builder
	header := func(name, value string) {
		b.WriteString(name + ": " + oneLine(value) + "\r\n")
	}
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", oneLine(m.Subject)))
	header("Date", now.Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	header("Content-Type", `text/plain; charset="utf-8"`)
	header("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")

	text := strings.ReplaceAll(m.Text, "\r\n", "\n")
	b.WriteString(strings.ReplaceAll(text, "\n", "\r\n"))
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

// oneLine removes line breaks, so a value cannot start a new header.
func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// Log writes each message to the log instead of sending it. It is for local development only:
// the log then holds whatever the message holds, for example a password reset link.
type Log struct {
	Log *slog.Logger
}

// Send logs m and never fails.
func (l Log) Send(ctx context.Context, m Message) error {
	l.Log.WarnContext(ctx, "email not sent: no mail server is configured", "to", m.To, "subject", m.Subject, "text", m.Text)
	return nil
}
