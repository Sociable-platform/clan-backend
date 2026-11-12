package handler

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/pkg/jwt"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/service"
)

// readCall is what a read method of the fake service was asked.
type readCall struct {
	who                       service.Reader
	conversationID            string
	before, limit, lastReadID string
}

// readFake holds the fake behind the REST reads and conversation.read; chatService embeds it.
type readFake struct {
	rmu       sync.Mutex
	readCalls []readCall
	list      func() ([]service.Conversation, error)
	page      func() (service.MessagePage, error)
	markRead  func() error
}

func (r *readFake) record(c readCall) {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	r.readCalls = append(r.readCalls, c)
}

func (r *readFake) recorded() []readCall {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	return append([]readCall(nil), r.readCalls...)
}

func (c *chatService) Conversations(_ context.Context, who service.Reader) ([]service.Conversation, error) {
	c.record(readCall{who: who})
	if c.list == nil {
		return nil, errors.New("no fake configured")
	}
	return c.list()
}

func (c *chatService) Messages(_ context.Context, who service.Reader, conversationID, before, limit string) (service.MessagePage, error) {
	c.record(readCall{who: who, conversationID: conversationID, before: before, limit: limit})
	if c.page == nil {
		return service.MessagePage{}, errors.New("no fake configured")
	}
	return c.page()
}

func (c *chatService) MarkRead(_ context.Context, who service.Reader, conversationID, lastMessageID string) error {
	c.record(readCall{who: who, conversationID: conversationID, lastReadID: lastMessageID})
	if c.markRead == nil {
		return errors.New("no fake configured")
	}
	return c.markRead()
}

// get calls the running service with an optional Authorization header and Origin.
func (e *wsEnv) get(t *testing.T, path, authorization, origin string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func bearer(token string) string { return "Bearer " + token }

func codeOf(t *testing.T, body []byte) string {
	t.Helper()
	var b struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("body %q is not JSON: %v", body, err)
	}
	return b.Error.Code
}

// u42 is what the fake social answers for the player "u_42" of the test game.
var u42 = socialIDFor(testGameID + "/u_42")

func TestConversationsEndpoint(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.list = func() ([]service.Conversation, error) {
		return []service.Conversation{{ID: convID, Kind: "dm", Members: []service.ConversationMember{{ID: u42, Name: "Name of u_42"}}, Unread: 3}}, nil
	}
	resp, body := env.get(t, "/api/v1/conversations", bearer(env.playerToken(t)), "")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, content-type %q: %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	want := `{"conversations":[{"id":"` + convID + `","kind":"dm","clanId":null,"members":[{"id":"` + u42 + `","name":"Name of u_42","online":false}],"lastMessage":null,"unread":3}]}`
	if strings.TrimSpace(string(body)) != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
	// The game is the token's and the player is social's ID for the token: nothing else the client sent counts.
	calls := env.chat.recorded()
	if len(calls) != 1 || calls[0].who != (service.Reader{GameID: testGameID, PlayerID: u42}) {
		t.Errorf("service got %+v, want the game of the token and social's ID of the player", calls)
	}
}

func TestConversationsEndpointWithNoConversationsIsAnEmptyList(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.list = func() ([]service.Conversation, error) { return []service.Conversation{}, nil }
	_, body := env.get(t, "/api/v1/conversations", bearer(env.playerToken(t)), "")
	if strings.TrimSpace(string(body)) != `{"conversations":[]}` {
		t.Errorf("body = %s", body)
	}
}

func TestMessagesEndpoint(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.page = func() (service.MessagePage, error) {
		return service.MessagePage{Messages: []service.Message{{
			ID: "0193a6c0-1111-7222-8333-944455556666", SenderID: u42, SenderName: "Name of u_42", Body: "gg",
			CreatedAt: time.Date(2026, 11, 9, 18, 20, 0, 0, time.UTC)}}, HasMore: true}, nil
	}
	resp, body := env.get(t, "/api/v1/conversations/"+convID+"/messages?before=0193a6c0-2222-7222-8333-944455556666&limit=20", bearer(env.playerToken(t)), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	want := `{"messages":[{"id":"0193a6c0-1111-7222-8333-944455556666","senderId":"` + u42 + `","senderName":"Name of u_42","body":"gg","createdAt":"2026-11-09T18:20:00Z"}],"hasMore":true}`
	if strings.TrimSpace(string(body)) != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
	calls := env.chat.recorded()
	wantCall := readCall{who: service.Reader{GameID: testGameID, PlayerID: u42}, conversationID: convID,
		before: "0193a6c0-2222-7222-8333-944455556666", limit: "20"}
	if len(calls) != 1 || calls[0] != wantCall {
		t.Errorf("service got %+v, want %+v (the raw query values; the service checks them)", calls, wantCall)
	}
}

func TestMessagesEndpointWithoutQuery(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.page = func() (service.MessagePage, error) { return service.MessagePage{Messages: []service.Message{}}, nil }
	_, body := env.get(t, "/api/v1/conversations/"+convID+"/messages", bearer(env.playerToken(t)), "")
	if strings.TrimSpace(string(body)) != `{"messages":[],"hasMore":false}` {
		t.Errorf("body = %s", body)
	}
	if calls := env.chat.recorded(); len(calls) != 1 || calls[0].before != "" || calls[0].limit != "" {
		t.Errorf("service got %+v, want empty before and limit", calls)
	}
}

func TestRESTErrorsFromTheServiceKeepTheirStatusAndCode(t *testing.T) {
	env := newWSEnv(t, nil)
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not a member", httpx.NewError(http.StatusForbidden, "not_member", "You are not a member of this conversation"), http.StatusForbidden, "not_member"},
		{"bad limit", httpx.NewError(http.StatusBadRequest, "invalid_limit", "limit must be a whole number from 1 to 100"), http.StatusBadRequest, "invalid_limit"},
		{"bad before", httpx.NewError(http.StatusBadRequest, "invalid_before", "before must be a message ID"), http.StatusBadRequest, "invalid_before"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env.chat.page = func() (service.MessagePage, error) { return service.MessagePage{}, tt.err }
			resp, body := env.get(t, "/api/v1/conversations/"+convID+"/messages", bearer(env.playerToken(t)), "")
			if resp.StatusCode != tt.status || codeOf(t, body) != tt.code {
				t.Errorf("= %d %s, want %d %s", resp.StatusCode, body, tt.status, tt.code)
			}
		})
	}
}

func TestRESTUnexpectedFailureIsAGeneric500(t *testing.T) {
	env := newWSEnv(t, nil)
	secret := "pq: password authentication failed for user realtime_user"
	env.chat.list = func() ([]service.Conversation, error) { return nil, errors.New(secret) }
	env.chat.page = func() (service.MessagePage, error) { return service.MessagePage{}, errors.New(secret) }
	for _, path := range []string{"/api/v1/conversations", "/api/v1/conversations/" + convID + "/messages"} {
		resp, body := env.get(t, path, bearer(env.playerToken(t)), "")
		if resp.StatusCode != http.StatusInternalServerError || codeOf(t, body) != "internal_error" || strings.Contains(string(body), "password") {
			t.Errorf("%s = %d %s, want a generic 500 internal_error without the cause", path, resp.StatusCode, body)
		}
	}
}

func TestRESTNeedsAValidPlayerToken(t *testing.T) {
	env := newWSEnv(t, nil)
	expired, err := jwt.Sign(env.priv, jwt.Claims{Subject: "u_42", GameID: testGameID, Type: jwt.TypePlayer}, time.Hour, time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		authorization string
		code          string
	}{
		{"no header", "", "missing_token"},
		{"not a bearer scheme", "Basic " + env.playerToken(t), "missing_token"},
		{"bearer without a token", "Bearer ", "missing_token"},
		{"garbage", bearer("not.a.token"), "invalid_token"},
		{"expired", bearer(expired), "token_expired"},
		{"developer token", bearer(env.token(t, jwt.Claims{Subject: "dev_1", Type: jwt.TypeDeveloper}, time.Hour)), "wrong_token_type"},
		{"signed by another key", bearer(signedByOtherKey(t)), "invalid_token"},
	}
	for _, path := range []string{"/api/v1/conversations", "/api/v1/conversations/" + convID + "/messages"} {
		for _, tt := range tests {
			t.Run(path+" "+tt.name, func(t *testing.T) {
				resp, body := env.get(t, path, tt.authorization, "")
				if resp.StatusCode != http.StatusUnauthorized || codeOf(t, body) != tt.code {
					t.Errorf("= %d %s, want 401 %s", resp.StatusCode, body, tt.code)
				}
				if resp.Header.Get("WWW-Authenticate") == "" {
					t.Error("no WWW-Authenticate header")
				}
				if len(env.chat.recorded()) != 0 {
					t.Error("the service was called without a valid token")
				}
			})
		}
	}
}

func TestRESTDoesNotTakeTheTokenFromTheQueryString(t *testing.T) {
	env := newWSEnv(t, nil)
	resp, body := env.get(t, "/api/v1/conversations?token="+env.playerToken(t), "", "")
	if resp.StatusCode != http.StatusUnauthorized || codeOf(t, body) != "missing_token" {
		t.Errorf("= %d %s, want 401 missing_token (tokens in URLs end up in logs)", resp.StatusCode, body)
	}
}

func TestRESTWhenSocialIsDown(t *testing.T) {
	env := newWSEnv(t, nil)
	env.socialDown.Store(true)
	resp, body := env.get(t, "/api/v1/conversations", bearer(env.playerToken(t)), "")
	if resp.StatusCode != http.StatusServiceUnavailable || codeOf(t, body) != "social_unavailable" {
		t.Errorf("= %d %s, want 503 social_unavailable", resp.StatusCode, body)
	}
	if len(env.chat.recorded()) != 0 {
		t.Error("the service was called although the player could not be identified")
	}
}

func TestRESTRefusesOtherOriginsAndNeverLogsTheToken(t *testing.T) {
	env := newWSEnv(t, nil)
	env.chat.list = func() ([]service.Conversation, error) { return []service.Conversation{}, nil }
	token := env.playerToken(t)

	resp, body := env.get(t, "/api/v1/conversations", bearer(token), "http://evil.example")
	if resp.StatusCode != http.StatusForbidden || codeOf(t, body) != "origin_not_allowed" {
		t.Errorf("other origin = %d %s, want 403 origin_not_allowed", resp.StatusCode, body)
	}
	resp, _ = env.get(t, "/api/v1/conversations", bearer(token), allowedOrigin)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("allowed origin = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(env.logs.String(), token) {
		t.Error("the token is in the logs")
	}
}

func TestRESTOnlyAllowsGet(t *testing.T) {
	env := newWSEnv(t, nil)
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/conversations", strings.NewReader("{}"))
	req.Header.Set("Authorization", bearer(env.playerToken(t)))
	resp, err := env.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("POST /api/v1/conversations = %d, want it refused", resp.StatusCode)
	}
}

// signedByOtherKey is a well-formed player token that platform's key did not sign.
func signedByOtherKey(t *testing.T) string {
	t.Helper()
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Sign(otherPriv, jwt.Claims{Subject: "u_42", GameID: testGameID, Type: jwt.TypePlayer}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
