package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/social/internal/service"
)

// The other fakes must satisfy the Service interface too.
func (f fakeService) BlockPlayer(context.Context, string, string, string, string) error { return f.err }

func (f fakeService) UnblockPlayer(context.Context, string, string, string, string) error {
	return f.err
}

func (f fakeService) ListBlocks(context.Context, string, string, string) ([]service.BlockedPlayer, error) {
	return nil, f.err
}

func (s *recordingService) BlockPlayer(context.Context, string, string, string, string) error {
	return s.err
}

func (s *recordingService) UnblockPlayer(context.Context, string, string, string, string) error {
	return s.err
}

func (s *recordingService) ListBlocks(context.Context, string, string, string) ([]service.BlockedPlayer, error) {
	return nil, s.err
}

// blockService remembers what the handler passed on and answers with what the test gave it.
type blockService struct {
	friendService
	blocks []service.BlockedPlayer
}

func (s *blockService) BlockPlayer(_ context.Context, gameID, externalID, name, playerID string) error {
	s.gameID, s.externalID, s.name, s.arg, s.called = gameID, externalID, name, playerID, "block"
	return s.err
}

func (s *blockService) UnblockPlayer(_ context.Context, gameID, externalID, name, playerID string) error {
	s.gameID, s.externalID, s.name, s.arg, s.called = gameID, externalID, name, playerID, "unblock"
	return s.err
}

func (s *blockService) ListBlocks(_ context.Context, gameID, externalID, name string) ([]service.BlockedPlayer, error) {
	s.gameID, s.externalID, s.name, s.called = gameID, externalID, name, "list"
	return s.blocks, s.err
}

func TestBlockAndUnblockAnswerNoContent(t *testing.T) {
	cases := []struct{ method, call string }{
		{http.MethodPost, "block"},
		{http.MethodDelete, "unblock"},
	}
	for _, c := range cases {
		t.Run(c.call, func(t *testing.T) {
			svc := &blockService{}
			rec := callFriends(t, svc, c.method, "/api/v1/players/"+friendID+"/block", "", false)
			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Fatalf("status %d, body %q; want 204 and no body", rec.Code, rec.Body.String())
			}
			svc.checkCaller(t)
			if svc.called != c.call || svc.arg != friendID {
				t.Errorf("service got %s(%q), want %s(%q)", svc.called, svc.arg, c.call, friendID)
			}
		})
	}
}

func TestBlockAndUnblockPassErrorsOn(t *testing.T) {
	errs := []*httpx.Error{
		httpx.NewError(http.StatusBadRequest, "cannot_block_self", "x"),
		httpx.NewError(http.StatusNotFound, "player_not_found", "x"),
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, e := range errs {
			rec := callFriends(t, &blockService{friendService: friendService{err: e}}, method,
				"/api/v1/players/"+friendID+"/block", "", false)
			if rec.Code != e.Status || !strings.Contains(rec.Body.String(), `"`+e.Code+`"`) {
				t.Errorf("%s: status %d, body %s; want %d %s", method, rec.Code, rec.Body.String(), e.Status, e.Code)
			}
		}
	}
}

func TestListBlocksHandler(t *testing.T) {
	// The service may hand back a time in another zone; the answer is always UTC.
	since := time.Date(2026, 11, 11, 13, 0, 0, 0, time.FixedZone("Baku", 4*3600))
	svc := &blockService{blocks: []service.BlockedPlayer{{
		FriendPlayer: service.FriendPlayer{ID: friendID, DisplayName: "Aysel"}, Since: since,
	}}}
	rec := callFriends(t, svc, http.MethodGet, "/api/v1/players/me/blocks", "", false)
	checkBody(t, rec, http.StatusOK, `{"blocks":[{"id":"`+friendID+`","displayName":"Aysel","since":"2026-11-11T09:00:00Z"}]}`)
	svc.checkCaller(t)
	if svc.called != "list" {
		t.Errorf("service got %q, want list", svc.called)
	}

	svc.blocks = nil
	checkBody(t, callFriends(t, svc, http.MethodGet, "/api/v1/players/me/blocks", "", false), http.StatusOK, `{"blocks":[]}`)
}

func TestBlockRoutesNeedAToken(t *testing.T) {
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/players/" + friendID + "/block"},
		{http.MethodDelete, "/api/v1/players/" + friendID + "/block"},
		{http.MethodGet, "/api/v1/players/me/blocks"},
	}
	for _, c := range cases {
		svc := &blockService{}
		rec := callFriends(t, svc, c.method, c.path, "", true)
		if rec.Code != http.StatusUnauthorized || svc.called != "" {
			t.Errorf("%s %s without a token: status %d, service called %q", c.method, c.path, rec.Code, svc.called)
		}
	}
}
