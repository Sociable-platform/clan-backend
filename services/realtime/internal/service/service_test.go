package service

import (
	"context"
	"errors"
	"testing"
)

type fakeStore struct{ err error }

func (f fakeStore) Ping(context.Context) error { return f.err }

func TestReady(t *testing.T) {
	errDown := errors.New("database down")
	tests := []struct {
		name    string
		store   fakeStore
		wantErr error
	}{
		{"database answers", fakeStore{}, nil},
		{"database down", fakeStore{err: errDown}, errDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New(tt.store).Ready(context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Ready() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
