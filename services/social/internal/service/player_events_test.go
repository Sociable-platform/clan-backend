package service

import (
	"context"
	"testing"
)

func TestEnsurePlayerPublishesPlayerRenamedOnlyOnARename(t *testing.T) {
	store := newMemStore()
	svc, rec := friendService(store)
	ctx := context.Background()

	for _, name := range []string{"Ali", "Ali", "  Ali  "} { // first call, then the same name (cleaned)
		if _, err := svc.EnsurePlayer(ctx, gameA, "u_42", name); err != nil {
			t.Fatal(err)
		}
	}
	if evs := rec.Events(); len(evs) != 0 {
		t.Fatalf("published %+v without a rename", evs)
	}

	p, err := svc.EnsurePlayer(ctx, gameA, "u_42", "Ali the Great")
	if err != nil {
		t.Fatal(err)
	}
	wantData(t, oneEvent(t, rec, EventPlayerRenamed), map[string]any{
		"player": ref(p.ID, "Ali the Great"),
	})
}

func TestPlayerRenamedHasTheFieldsOfTheEventMessagesFile(t *testing.T) {
	store := newMemStore()
	svc, rec := friendService(store)
	ctx := context.Background()
	if _, err := svc.EnsurePlayer(ctx, gameA, "u_42", "Ali"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsurePlayer(ctx, gameA, "u_42", "Aliyev"); err != nil {
		t.Fatal(err)
	}
	evs := rec.Events()
	if len(evs) != 1 || evs[0].Type != EventPlayerRenamed {
		t.Fatalf("events = %+v, want one %s", evs, EventPlayerRenamed)
	}
	want, ok := docShapes(t)[EventPlayerRenamed]
	if !ok {
		t.Fatalf("%s is not in docs/event-messages.md", EventPlayerRenamed)
	}
	if got := publishedShape(t, evs[0]); got != want {
		t.Errorf("fields of %s:\n  published %s\n  file      %s", EventPlayerRenamed, got, want)
	}
}
