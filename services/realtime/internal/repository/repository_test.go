package repository

import (
	"context"
	"errors"
	"testing"
)

func TestPingWithoutDatabase(t *testing.T) {
	err := New(nil).Ping(context.Background())
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("Ping without a pool = %v, want ErrNoDatabase", err)
	}
}
