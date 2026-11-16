package main

import (
	"context"
	"log/slog"

	"gitlab.com/clan-platform/clan-backend/pkg/mailer"
)

// verificationMailer sends the emails of registering with email verification (it is the
// service's VerificationSender). Like resetMailer it sends in the background, so a slow or broken
// mail server neither delays the answer nor shows in it, and a failure is only logged, without
// the address or the code.
type verificationMailer struct {
	sender mailer.Sender
	log    *slog.Logger
	// dashboardURL is where the dashboard runs, for the links in the emails.
	dashboardURL string
}

func (m verificationMailer) send(what string, msg mailer.Message) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), resetMailTimeout)
		defer cancel()
		if err := m.sender.Send(ctx, msg); err != nil {
			m.log.Error(what+" email was not sent", "error", err)
		}
	}()
}

// SendVerificationCode emails the code that finishes a registration.
func (m verificationMailer) SendVerificationCode(to, name, code string) {
	m.send("verification code", mailer.Message{
		To:      to,
		Subject: "Your Sociable verification code",
		Text: "Hi " + name + ",\n\n" +
			"Enter this code to finish creating your Sociable developer account:\n\n" +
			"    " + code + "\n\n" +
			"The code works for 15 minutes.\n" +
			"If you did not register, ignore this email: no account is created without the code.\n\n" +
			"Sociable\n",
	})
}

// SendAlreadyRegistered tells the owner of an account that someone tried to register with its
// email address.
func (m verificationMailer) SendAlreadyRegistered(to, name string) {
	m.send("already registered", mailer.Message{
		To:      to,
		Subject: "You already have a Sociable account",
		Text: "Hi " + name + ",\n\n" +
			"Someone tried to register a Sociable developer account with this email address,\n" +
			"but it already has one. Nothing was changed.\n\n" +
			"If it was you, log in here:\n" +
			m.dashboardURL + "/login\n\n" +
			"If you forgot your password, choose a new one here:\n" +
			m.dashboardURL + "/forgot-password\n\n" +
			"If it was not you, ignore this email.\n\n" +
			"Sociable\n",
	})
}
