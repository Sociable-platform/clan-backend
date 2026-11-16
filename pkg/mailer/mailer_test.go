package mailer

import (
	"bytes"
	"context"
	"log/slog"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestBuildMessage(t *testing.T) {
	from := &mail.Address{Name: "Sociable", Address: "noreply@example.com"}
	to := &mail.Address{Address: "dev@example.com"}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	raw := string(buildMessage(from, to, Message{
		To:      "dev@example.com",
		Subject: "Reset your password\r\nBcc: someone@example.com",
		Text:    "Hello,\nopen the link.\n",
	}, now))

	head, text, found := strings.Cut(raw, "\r\n\r\n")
	if !found {
		t.Fatalf("no empty line between the headers and the text:\n%s", raw)
	}
	if text != "Hello,\r\nopen the link.\r\n" {
		t.Fatalf("text %q", text)
	}
	for _, want := range []string{
		`From: "Sociable" <noreply@example.com>`,
		"To: <dev@example.com>",
		"Subject: Reset your password  Bcc: someone@example.com",
		"Date: Mon, 16 Nov 2026 12:00:00 +0000",
		`Content-Type: text/plain; charset="utf-8"`,
	} {
		if !strings.Contains(head, want+"\r\n") && !strings.HasSuffix(head, want) {
			t.Errorf("header %q is missing in:\n%s", want, head)
		}
	}
	// The line break in the subject must not have become a header of its own.
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Fatalf("the subject added a header: %q", line)
		}
	}
}

func TestLogSenderWritesTheMessage(t *testing.T) {
	var out bytes.Buffer
	l := Log{Log: slog.New(slog.NewTextHandler(&out, nil))}
	if err := l.Send(context.Background(), Message{To: "dev@example.com", Subject: "Hi", Text: "the link"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dev@example.com") || !strings.Contains(out.String(), "the link") {
		t.Fatalf("log output %q", out.String())
	}
}

func TestSMTPRefusesABadSenderAddress(t *testing.T) {
	err := SMTP{Host: "localhost", Port: 587, From: "not an address"}.Send(context.Background(), Message{To: "dev@example.com"})
	if err == nil || !strings.Contains(err.Error(), "sender address") {
		t.Fatalf("got %v", err)
	}
}
