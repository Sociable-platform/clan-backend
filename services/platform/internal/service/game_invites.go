package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/platform/internal/repository"
)

const (
	// gameInviteTTL is how long an invite can be accepted.
	gameInviteTTL = 7 * 24 * time.Hour
	// maxInvitesPerGameHour limits what one game may send in an hour, to anybody.
	maxInvitesPerGameHour = 20
	// maxInvitesPerPairDay limits what one game may send to one address in a day: a renewal counts.
	maxInvitesPerPairDay = 3
	// maxInvitesPerAddressDay limits what one address receives in a day from all games together, so the
	// invitations cannot be used to fill somebody's inbox.
	maxInvitesPerAddressDay = 5
)

// InviteSender delivers the invitation emails. It may not block: the request is answered at once, whether
// or not an email goes out.
type InviteSender interface {
	// SendGameInvite tells the address that inviterName invited them to the game. hasAccount says whether a
	// developer account with that address exists, which decides whether the email says "log in" or "register".
	SendGameInvite(to, inviterName, gameName string, hasAccount bool)
}

// noInviteSender is used until WithInvites is called: invites are stored but no email is sent.
type noInviteSender struct{}

func (noInviteSender) SendGameInvite(string, string, string, bool) {}

// WithInvites sets what sends the invitation emails. It returns s, so it can follow New.
func (s *Service) WithInvites(sender InviteSender) *Service {
	s.inviteSender = sender
	return s
}

// GameInvite is a pending invitation.
type GameInvite struct {
	ID            string
	GameID        string
	GameName      string
	Email         string
	InvitedByName string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func toGameInvite(i repository.GameInvite) GameInvite {
	return GameInvite{ID: i.ID, GameID: i.GameID, GameName: i.GameName, Email: i.Email, InvitedByName: i.InvitedByName,
		CreatedAt: i.CreatedAt, ExpiresAt: i.ExpiresAt}
}

func errInviteNotFound() *httpx.Error {
	return httpx.NewError(http.StatusNotFound, "invite_not_found", "Invitation not found")
}

func errInviteExpired() *httpx.Error {
	return httpx.NewError(http.StatusGone, "invite_expired", "This invitation has expired. Ask the game's owner to send a new one.")
}

func errAlreadyMember() *httpx.Error {
	return httpx.NewError(http.StatusConflict, "already_member", "This developer is already on the game")
}

func errInviteRateLimited(message string) *httpx.Error {
	return httpx.NewError(http.StatusTooManyRequests, "rate_limited", message)
}

// InviteToGame invites the person with this email to the game, for an owner of it (a member gets 403
// owner_required). The invite is a row for the address; the person sees it by logging in with an account that
// has it, and the email says so. Inviting an address that is invited already renews that invite (a new
// expiry) and sends the email again. A developer who is on the game already gives 409 already_member.
//
// Sending is limited (maxInvitesPerGameHour, maxInvitesPerPairDay, maxInvitesPerAddressDay): over a limit is
// 429 rate_limited. The response is the same whether the address has an account or not, so inviting cannot
// be used to find out who has one.
func (s *Service) InviteToGame(ctx context.Context, developerID, gameID, email string) (GameInvite, error) {
	g, err := s.OwnerGame(ctx, developerID, gameID)
	if err != nil {
		return GameInvite{}, err
	}
	email = normalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return GameInvite{}, err
	}
	inviter, err := s.store.DeveloperByID(ctx, developerID)
	if err != nil {
		return GameInvite{}, fmt.Errorf("find inviter %s: %w", developerID, err)
	}

	now := s.now()
	var saved repository.GameInvite
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.PurgeGameInvites(ctx, now); err != nil {
			return fmt.Errorf("purge game invites: %w", err)
		}
		member, err := s.store.EmailIsMember(ctx, gameID, email)
		if err != nil {
			return fmt.Errorf("check member of game %s: %w", gameID, err)
		}
		if member {
			return errAlreadyMember()
		}
		c, err := s.store.InviteCountsSince(ctx, gameID, email, now)
		if err != nil {
			return fmt.Errorf("count invites of game %s: %w", gameID, err)
		}
		switch {
		case c.GameLastHour >= maxInvitesPerGameHour:
			return errInviteRateLimited("This game sent many invitations in the last hour. Try again later.")
		case c.PairLastDay >= maxInvitesPerPairDay:
			return errInviteRateLimited("This person was invited to this game several times today. Try again tomorrow.")
		case c.AddressLastDay >= maxInvitesPerAddressDay:
			return errInviteRateLimited("This person received many invitations today. Try again tomorrow.")
		}
		saved, err = s.store.SaveGameInvite(ctx, gameID, email, developerID, now, now.Add(gameInviteTTL))
		if err != nil {
			return fmt.Errorf("save invite to game %s: %w", gameID, err)
		}
		return s.logTeam(ctx, gameID, repository.TeamLogInput{Action: repository.TeamInviteSent, ActorID: developerID, SubjectEmail: email})
	})
	if err != nil {
		return GameInvite{}, err
	}

	// Whether the address has an account only changes the wording of the email.
	_, lookup := s.store.DeveloperByEmail(ctx, email)
	s.inviteSender.SendGameInvite(email, inviter.Name, g.Name, lookup == nil)
	return toGameInvite(saved), nil
}

// GameInvites lists the invites of the game that are pending and have not expired, newest first, for an owner.
func (s *Service) GameInvites(ctx context.Context, developerID, gameID string) ([]GameInvite, error) {
	if _, err := s.OwnerGame(ctx, developerID, gameID); err != nil {
		return nil, err
	}
	rows, err := s.store.GameInvitesOfGame(ctx, gameID, s.now())
	if err != nil {
		return nil, fmt.Errorf("list invites of game %s: %w", gameID, err)
	}
	return toGameInvites(rows), nil
}

// CancelGameInvite takes an invite back, for an owner. An invite that does not exist or belongs to another
// game is 404 invite_not_found.
func (s *Service) CancelGameInvite(ctx context.Context, developerID, gameID, inviteID string) error {
	if _, err := s.OwnerGame(ctx, developerID, gameID); err != nil {
		return err
	}
	if !uuidPattern.MatchString(inviteID) {
		return errInviteNotFound()
	}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		email, err := s.store.DeleteGameInvite(ctx, gameID, inviteID)
		if err != nil {
			return err
		}
		return s.logTeam(ctx, gameID, repository.TeamLogInput{Action: repository.TeamInviteCancelled, ActorID: developerID, SubjectEmail: email})
	})
	if errors.Is(err, repository.ErrNotFound) {
		return errInviteNotFound()
	}
	if err != nil {
		return fmt.Errorf("cancel invite %s: %w", inviteID, err)
	}
	return nil
}

// MyInvites lists the pending, unexpired invites sent to the logged-in developer's email address. An invite is
// for an address, so it shows for the account that has that address and for nobody else.
func (s *Service) MyInvites(ctx context.Context, developerID string) ([]GameInvite, error) {
	d, err := s.store.DeveloperByID(ctx, developerID)
	if err != nil {
		return nil, fmt.Errorf("find developer %s: %w", developerID, err)
	}
	rows, err := s.store.GameInvitesForEmail(ctx, normalizeEmail(d.Email), s.now())
	if err != nil {
		return nil, fmt.Errorf("list invites of %s: %w", developerID, err)
	}
	return toGameInvites(rows), nil
}

// AcceptInvite makes the logged-in developer a member of the game they were invited to. The invite must have
// been sent to their email address: somebody else's invite and an invite that does not exist are both 404
// invite_not_found. One that is theirs but past its time is 410 invite_expired. Accepting creates the member
// row and deletes the invite in one statement. The result is the game, with role member.
func (s *Service) AcceptInvite(ctx context.Context, developerID, inviteID string) (Game, error) {
	d, err := s.store.DeveloperByID(ctx, developerID)
	if err != nil {
		return Game{}, fmt.Errorf("find developer %s: %w", developerID, err)
	}
	if !uuidPattern.MatchString(inviteID) {
		return Game{}, errInviteNotFound()
	}
	email := normalizeEmail(d.Email)
	var g repository.Game
	var expired bool
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		var err error
		g, err = s.store.AcceptGameInvite(ctx, inviteID, developerID, email, s.now())
		if errors.Is(err, repository.ErrInviteExpired) {
			// The expired invite was deleted and nobody joined. Keep the deletion (return nil), answer 410 below.
			expired = true
			return nil
		}
		if err != nil {
			return err
		}
		return s.logTeam(ctx, g.ID, repository.TeamLogInput{
			Action: repository.TeamInviteAccepted, ActorID: developerID, SubjectID: developerID, SubjectEmail: email})
	})
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return Game{}, errInviteNotFound()
	case err != nil:
		return Game{}, fmt.Errorf("accept invite %s: %w", inviteID, err)
	case expired:
		return Game{}, errInviteExpired()
	}
	return toGame(g), nil
}

// DeclineInvite deletes an invite sent to the logged-in developer's address; nothing else happens. Somebody
// else's invite and one that does not exist are 404 invite_not_found.
func (s *Service) DeclineInvite(ctx context.Context, developerID, inviteID string) error {
	d, err := s.store.DeveloperByID(ctx, developerID)
	if err != nil {
		return fmt.Errorf("find developer %s: %w", developerID, err)
	}
	if !uuidPattern.MatchString(inviteID) {
		return errInviteNotFound()
	}
	email := normalizeEmail(d.Email)
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		gameID, err := s.store.DeclineGameInvite(ctx, inviteID, email)
		if err != nil {
			return err
		}
		return s.logTeam(ctx, gameID, repository.TeamLogInput{Action: repository.TeamInviteDeclined, ActorID: developerID, SubjectEmail: email})
	})
	if errors.Is(err, repository.ErrNotFound) {
		return errInviteNotFound()
	}
	if err != nil {
		return fmt.Errorf("decline invite %s: %w", inviteID, err)
	}
	return nil
}

func toGameInvites(rows []repository.GameInvite) []GameInvite {
	out := make([]GameInvite, 0, len(rows))
	for _, r := range rows {
		out = append(out, toGameInvite(r))
	}
	return out
}
