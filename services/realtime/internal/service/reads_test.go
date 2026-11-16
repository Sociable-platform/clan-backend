package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

const (
	conv1 = "0193a6b4-7c1e-7d2a-9f10-2b3c4d5e6f70"
	msgA  = "0193a6c0-1111-7222-8333-944455556666"
)

func (m *memStore) ListConversations(_ context.Context, gameID, playerID string) ([]repository.PlayerConversation, error) {
	m.readCalls = append(m.readCalls, "list "+gameID+" "+playerID)
	return m.convs, m.err
}

func (m *memStore) ListMessages(_ context.Context, gameID, playerID, conversationID, before string, limit int) ([]repository.Message, bool, error) {
	m.readCalls = append(m.readCalls, fmt.Sprintf("messages %s %s %s before=%q limit=%d", gameID, playerID, conversationID, before, limit))
	if m.readErr != nil {
		return nil, false, m.readErr
	}
	return m.msgs, m.hasMore, m.err
}

func (m *memStore) MarkRead(_ context.Context, gameID, playerID, conversationID, messageID string) error {
	m.readCalls = append(m.readCalls, "read "+gameID+" "+playerID+" "+conversationID+" "+messageID)
	if m.readErr != nil {
		return m.readErr
	}
	return m.err
}

func wantHTTP(t *testing.T, err error, status int, code string) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != status || he.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
}

var reader = Reader{GameID: game, PlayerID: ali}

func TestConversationsBuildsTheListForTheReader(t *testing.T) {
	at := time.Date(2026, 11, 9, 18, 20, 0, 0, time.FixedZone("x", 4*3600))
	clan := "3b0c7a52-6d1e-4f0a-9c2b-1e2d3c4b5a69"
	store := &memStore{convs: []repository.PlayerConversation{
		{ID: conv1, Kind: "dm", Members: []repository.Member{{PlayerID: ali, Name: "Ali"}, {PlayerID: rashad, Name: "Rashad"}},
			LastMessage: &repository.Message{ID: msgA, Kind: "text", SenderID: rashad, SenderName: "Rashad", Body: "gg", CreatedAt: at}, Unread: 2},
		{ID: "0193a6b9-0000-7aaa-8bbb-ccccddddeeee", Kind: "clan", ClanID: &clan, Members: []repository.Member{{PlayerID: ali, Name: "Ali"}}},
	}}
	got, err := New(store, WithPresence(onlineSet{rashad: true})).Conversations(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"list " + game + " " + ali}; !reflect.DeepEqual(store.readCalls, want) {
		t.Errorf("store calls = %v, want %v (the game and the player come from the reader)", store.readCalls, want)
	}
	want := `[{"id":"` + conv1 + `","kind":"dm","clanId":null,"members":[` +
		`{"id":"` + ali + `","name":"Ali","online":false},{"id":"` + rashad + `","name":"Rashad","online":true}],` +
		`"lastMessage":{"id":"` + msgA + `","kind":"text","senderId":"` + rashad + `","senderName":"Rashad","body":"gg","createdAt":"2026-11-09T14:20:00Z"},"unread":2},` +
		`{"id":"0193a6b9-0000-7aaa-8bbb-ccccddddeeee","kind":"clan","clanId":"` + clan + `","members":[{"id":"` + ali + `","name":"Ali","online":false}],` +
		`"lastMessage":null,"unread":0}]`
	if s := mustJSON(t, got); s != want {
		t.Errorf("conversations =\n%s\nwant\n%s", s, want)
	}
}

func TestConversationsReadOnlyDMHidesPresence(t *testing.T) {
	// A block ended the friendship (TEAM-283): the DM is listed with readOnly, and nobody shows as online.
	store := &memStore{convs: []repository.PlayerConversation{
		{ID: conv1, Kind: "dm", Members: []repository.Member{{PlayerID: ali, Name: "Ali"}, {PlayerID: rashad, Name: "Rashad"}}, ReadOnly: true},
	}}
	got, err := New(store, WithPresence(onlineSet{ali: true, rashad: true})).Conversations(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"id":"` + conv1 + `","kind":"dm","clanId":null,"members":[` +
		`{"id":"` + ali + `","name":"Ali","online":false},{"id":"` + rashad + `","name":"Rashad","online":false}],` +
		`"lastMessage":null,"unread":0,"readOnly":true}]`
	if s := mustJSON(t, got); s != want {
		t.Errorf("conversations =\n%s\nwant\n%s", s, want)
	}
}

func TestConversationsWithNoneIsAnEmptyListNotNull(t *testing.T) {
	got, err := New(&memStore{}).Conversations(context.Background(), reader)
	if err != nil || got == nil || mustJSON(t, got) != "[]" {
		t.Errorf("= (%s, %v), want []", mustJSON(t, got), err)
	}
}

func TestConversationsDatabaseFailureIsNotAnHTTPError(t *testing.T) {
	errDB := errors.New("connection refused")
	_, err := New(&memStore{err: errDB}).Conversations(context.Background(), reader)
	var he *httpx.Error
	if !errors.Is(err, errDB) || errors.As(err, &he) {
		t.Errorf("error = %v, want the database error, not an expected failure", err)
	}
}

func TestMessagesPageAndQueryChecks(t *testing.T) {
	at := time.Date(2026, 11, 9, 18, 20, 0, 0, time.UTC)
	store := &memStore{hasMore: true, msgs: []repository.Message{
		{ID: msgA, Kind: "text", SenderID: rashad, SenderName: "Rashad", Body: "gg", CreatedAt: at},
	}}
	upper := strings.ToUpper(msgA)
	page, err := New(store).Messages(context.Background(), reader, strings.ToUpper(conv1), upper, " 20 ")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("messages %s %s %s before=%q limit=20", game, ali, conv1, msgA)
	if !reflect.DeepEqual(store.readCalls, []string{want}) {
		t.Errorf("store calls = %v, want [%s] (ids lower-cased, limit parsed)", store.readCalls, want)
	}
	if s := mustJSON(t, page); s != `{"messages":[{"id":"`+msgA+`","kind":"text","senderId":"`+rashad+`","senderName":"Rashad","body":"gg","createdAt":"2026-11-09T18:20:00Z"}],"hasMore":true}` {
		t.Errorf("page = %s", s)
	}
}

func TestMessagesDefaultsToTheNewestPageOfFifty(t *testing.T) {
	store := &memStore{}
	page, err := New(store).Messages(context.Background(), reader, conv1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("messages %s %s %s before=\"\" limit=50", game, ali, conv1); !reflect.DeepEqual(store.readCalls, []string{want}) {
		t.Errorf("store calls = %v, want [%s]", store.readCalls, want)
	}
	if mustJSON(t, page) != `{"messages":[],"hasMore":false}` {
		t.Errorf("page = %s, want an empty list (not null) and no more", mustJSON(t, page))
	}
}

func TestMessagesRejectsBadInputWithoutTouchingTheDatabase(t *testing.T) {
	tests := []struct {
		name, conv, before, limit string
		status                    int
		code                      string
	}{
		{"limit zero", conv1, "", "0", http.StatusBadRequest, "invalid_limit"},
		{"limit negative", conv1, "", "-5", http.StatusBadRequest, "invalid_limit"},
		{"limit over 100", conv1, "", "101", http.StatusBadRequest, "invalid_limit"},
		{"limit not a number", conv1, "", "abc", http.StatusBadRequest, "invalid_limit"},
		{"limit a fraction", conv1, "", "1.5", http.StatusBadRequest, "invalid_limit"},
		{"limit too big for an int", conv1, "", "99999999999999999999", http.StatusBadRequest, "invalid_limit"},
		{"before is not a uuid", conv1, "yesterday", "", http.StatusBadRequest, "invalid_before"},
		{"before has a bad character", conv1, "0193a6c0-1111-7222-8333-94445555666g", "", http.StatusBadRequest, "invalid_before"},
		{"conversation is not a uuid", "nope", "", "", http.StatusForbidden, "not_member"},
		{"conversation is empty", "", "", "", http.StatusForbidden, "not_member"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{}
			_, err := New(store).Messages(context.Background(), reader, tt.conv, tt.before, tt.limit)
			wantHTTP(t, err, tt.status, tt.code)
			if len(store.readCalls) != 0 {
				t.Errorf("the database was called: %v", store.readCalls)
			}
		})
	}
}

func TestMessagesAcceptsTheLimitsOfTheContract(t *testing.T) {
	for _, limit := range []string{"1", "100"} {
		if _, err := New(&memStore{}).Messages(context.Background(), reader, conv1, "", limit); err != nil {
			t.Errorf("limit %s: %v", limit, err)
		}
	}
}

func TestMessagesNotAMemberAndDatabaseFailure(t *testing.T) {
	_, err := New(&memStore{readErr: repository.ErrNotMember}).Messages(context.Background(), reader, conv1, "", "")
	wantHTTP(t, err, http.StatusForbidden, "not_member")

	errDB := errors.New("connection refused")
	_, err = New(&memStore{readErr: errDB}).Messages(context.Background(), reader, conv1, "", "")
	var he *httpx.Error
	if !errors.Is(err, errDB) || errors.As(err, &he) {
		t.Errorf("error = %v, want the database error, not an expected failure", err)
	}
}

func TestMarkRead(t *testing.T) {
	t.Run("moves the position with clean ids", func(t *testing.T) {
		store := &memStore{}
		if err := New(store).MarkRead(context.Background(), reader, strings.ToUpper(conv1), " "+strings.ToUpper(msgA)+" "); err != nil {
			t.Fatal(err)
		}
		want := "read " + game + " " + ali + " " + conv1 + " " + msgA
		if !reflect.DeepEqual(store.readCalls, []string{want}) {
			t.Errorf("store calls = %v, want [%s]", store.readCalls, want)
		}
	})
	t.Run("input the player got wrong never reaches the database", func(t *testing.T) {
		for _, c := range []struct {
			name, conv, msg string
			status          int
			code            string
		}{
			{"conversation not a uuid", "nope", msgA, http.StatusForbidden, "not_member"},
			{"message not a uuid", conv1, "latest", http.StatusBadRequest, "bad_frame"},
			{"message empty", conv1, "", http.StatusBadRequest, "bad_frame"},
		} {
			store := &memStore{}
			wantHTTP(t, New(store).MarkRead(context.Background(), reader, c.conv, c.msg), c.status, c.code)
			if len(store.readCalls) != 0 {
				t.Errorf("%s: the database was called", c.name)
			}
		}
	})
	t.Run("what the repository says becomes what the player gets", func(t *testing.T) {
		wantHTTP(t, New(&memStore{readErr: repository.ErrNotMember}).MarkRead(context.Background(), reader, conv1, msgA), http.StatusForbidden, "not_member")
		wantHTTP(t, New(&memStore{readErr: repository.ErrNoSuchMessage}).MarkRead(context.Background(), reader, conv1, msgA), http.StatusBadRequest, "bad_frame")
	})
	t.Run("a database failure is not an expected failure", func(t *testing.T) {
		errDB := errors.New("connection refused")
		err := New(&memStore{readErr: errDB}).MarkRead(context.Background(), reader, conv1, msgA)
		var he *httpx.Error
		if !errors.Is(err, errDB) || errors.As(err, &he) {
			t.Errorf("error = %v, want the database error", err)
		}
	})
}
