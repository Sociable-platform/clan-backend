package main

import (
	"context"
	"log/slog"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/mailer"
)

// resetMailTimeout is how long one password reset email may take to reach the mail server.
const resetMailTimeout = 30 * time.Second

// resetMailer sends the password reset emails (it is the service's ResetSender). Each email is
// sent in the background: the request that asked for it has been answered by then, so a slow or
// broken mail server neither delays the answer nor shows in it.
type resetMailer struct {
	sender mailer.Sender
	log    *slog.Logger
}

// SendPasswordReset emails the reset link to a developer. A failure is only logged, without the
// address or the link.
func (m resetMailer) SendPasswordReset(to, name, link string) {
	msg := mailer.Message{
		To:      to,
		Subject: "Reset your Sociable password",
		Text: "Hi " + name + ",\n\n" +
			"Someone asked to reset the password of your Sociable developer account.\n" +
			"Open this link to choose a new password:\n\n" +
			link + "\n\n" +
			"The link works once and for 30 minutes.\n" +
			"If it was not you, ignore this email: your password stays as it is.\n\n" +
			"Sociable\n",
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), resetMailTimeout)
		defer cancel()
		if err := m.sender.Send(ctx, msg); err != nil {
			m.log.Error("password reset email was not sent", "error", err)
		}
	}()
}
