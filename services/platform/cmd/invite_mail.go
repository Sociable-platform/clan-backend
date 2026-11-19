package main

import "gitlab.com/clan-platform/clan-backend/pkg/mailer"

// inviteMailer sends the game invitation emails (it is the service's InviteSender). It is the verification
// mailer with one more email, so it sends the way registration emails do: in the background, through the same
// SMTP server, and a failure is only logged, without the address.
type inviteMailer struct {
	verificationMailer
}

// SendGameInvite tells an address that a developer invited them to a game. The invitation is not in the link:
// it waits for the address in the dashboard, so a person with an account logs in and one without registers
// first, and nobody can accept an invitation by holding the email.
func (m inviteMailer) SendGameInvite(to, inviterName, gameName string, hasAccount bool) {
	next := "Create your free developer account with this email address, then accept the invitation:\n" +
		m.dashboardURL + "/register\n\n"
	if hasAccount {
		next = "Log in and accept the invitation:\n" + m.dashboardURL + "/login\n\n"
	}
	m.send("game invitation", mailer.Message{
		To:      to,
		Subject: inviterName + " invited you to the game " + gameName + " on Sociable",
		Text: "Hi,\n\n" +
			inviterName + " invited you to work on the game \"" + gameName + "\" on Sociable: you can edit its widget and publish it.\n\n" +
			next +
			"The invitation works for 7 days, and only for an account with this email address.\n" +
			"If you do not know " + inviterName + ", ignore this email: nothing happens unless you accept.\n\n" +
			"Sociable\n",
	})
}
