package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/repository"
)

const convA = "0193a6b4-7c1e-7d2a-9f10-2b3c4d5e6f70"

type fixedIDs struct {
	id string
	at time.Time
}

func (f fixedIDs) New() (string, time.Time) { return f.id, f.at }

var testNow = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

func newChat(store *memStore) *Service {
	return New(store, WithIDs(fixedIDs{id: "0193a6c0-1111-7222-8333-944455556666", at: testNow}))
}

var sender = Sender{GameID: game, ID: ali, Name: "Ali"}

func TestSendMessageStoresAndReturnsTheMembers(t *testing.T) {
	store := &memStore{members: []string{ali, rashad}}
	sent, err := newChat(store).SendMessage(context.Background(), sender, strings.ToUpper(convA), "  gg wp \n")
	if err != nil {
		t.Fatal(err)
	}

	if len(store.inserts) != 1 {
		t.Fatalf("inserts = %d, want 1", len(store.inserts))
	}
	got := store.inserts[0]
	want := insertCall{game, convA, ali, repository.NewMessage{
		ID: "0193a6c0-1111-7222-8333-944455556666", SenderName: "Ali", Body: "gg wp", CreatedAt: testNow}}
	if got != want {
		t.Errorf("insert =\n%+v\nwant\n%+v (body trimmed, conversation ID in lower case, game and sender from the Sender)", got, want)
	}

	wantMsg := Message{ID: "0193a6c0-1111-7222-8333-944455556666", SenderID: ali, SenderName: "Ali", Body: "gg wp", CreatedAt: testNow}
	if sent.Message != wantMsg {
		t.Errorf("message = %+v, want %+v", sent.Message, wantMsg)
	}
	if !reflect.DeepEqual(sent.Members, []string{ali, rashad}) {
		t.Errorf("members = %v, want everybody in the conversation", sent.Members)
	}
}

func TestSendMessageRejectsWithoutStoring(t *testing.T) {
	tests := []struct {
		name     string
		conv     string
		body     string
		wantCode string
	}{
		{"empty", convA, "", CodeEmpty},
		{"only spaces and newlines", convA, " \n\t  ", CodeEmpty},
		{"NUL character", convA, "hi\x00there", CodeBadFrame},
		{"1001 characters", convA, strings.Repeat("a", 1001), CodeTooLong},
		{"1001 emoji", convA, strings.Repeat("😀", 1001), CodeTooLong},
		{"conversation is not a UUID", "general", "hi", CodeNotMember},
		{"conversation is empty", "", "hi", CodeNotMember},
		{"conversation has a bad character", "0193a6b4-7c1e-7d2a-9f10-2b3c4d5e6f7g", "hi", CodeNotMember},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memStore{members: []string{ali}}
			_, err := newChat(store).SendMessage(context.Background(), sender, tt.conv, tt.body)
			var se *SendError
			if !errors.As(err, &se) || se.Code != tt.wantCode || se.Message == "" {
				t.Fatalf("error = %v, want a SendError with code %s", err, tt.wantCode)
			}
			if len(store.inserts) != 0 {
				t.Error("the message was stored although it was rejected")
			}
		})
	}
}

func TestSendMessageLengthCountsCharactersNotBytes(t *testing.T) {
	for name, body := range map[string]string{
		"1000 letters":                 strings.Repeat("a", 1000),
		"1000 emoji":                   strings.Repeat("😀", 1000), // 4000 bytes
		"1000 cyrillic after trimming": "  " + strings.Repeat("ə", 1000) + "  ",
	} {
		t.Run(name, func(t *testing.T) {
			store := &memStore{members: []string{ali}}
			if _, err := newChat(store).SendMessage(context.Background(), sender, convA, body); err != nil {
				t.Fatalf("error = %v, want the message accepted", err)
			}
		})
	}
}

func TestSendMessageNotAMember(t *testing.T) {
	store := &memStore{insertErr: repository.ErrNotMember}
	_, err := newChat(store).SendMessage(context.Background(), sender, convA, "hi")
	var se *SendError
	if !errors.As(err, &se) || se.Code != CodeNotMember {
		t.Fatalf("error = %v, want not_member", err)
	}
}

func TestSendMessageDatabaseFailureIsOurError(t *testing.T) {
	errDB := errors.New("connection refused")
	store := &memStore{insertErr: errDB}
	sent, err := newChat(store).SendMessage(context.Background(), sender, convA, "hi")
	var se *SendError
	if errors.As(err, &se) || !errors.Is(err, errDB) {
		t.Fatalf("error = %v, want the database error and not a SendError", err)
	}
	if sent.Members != nil || sent.Message.ID != "" {
		t.Errorf("sent = %+v, want nothing", sent)
	}
}
