package handler

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeConfigVersions answers Version from a map and records the games it was asked about. With wait set it
// blocks until the context ends, like a Platform that does not answer.
type fakeConfigVersions struct {
	mu       sync.Mutex
	versions map[string]int
	asked    []string
	wait     bool
}

func (f *fakeConfigVersions) Version(ctx context.Context, gameID string) int {
	f.mu.Lock()
	f.asked = append(f.asked, gameID)
	v, wait := f.versions[gameID], f.wait
	f.mu.Unlock()
	if wait {
		<-ctx.Done()
		return 0
	}
	return v
}

func TestHelloHasTheConfigVersionOfTheTokensGame(t *testing.T) {
	cfg := &fakeConfigVersions{versions: map[string]int{testGameID: 12, otherGameID: 99}}
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.Config = cfg })
	_, hello := env.connect(t, env.tokenFor(t, "alice"))
	if hello["configVersion"] != float64(12) {
		t.Errorf("configVersion = %v, want 12", hello["configVersion"])
	}
	if cfg.asked[0] != testGameID {
		t.Errorf("asked for game %v, want the game of the token", cfg.asked)
	}
}

func TestHelloHasConfigVersionZeroWithoutAConfigSource(t *testing.T) {
	env := newWSEnv(t, nil)
	_, hello := env.connect(t, env.tokenFor(t, "alice"))
	if hello["configVersion"] != float64(0) {
		t.Errorf("configVersion = %v, want 0", hello["configVersion"])
	}
}

func TestHelloDoesNotWaitLongForAConfigThatDoesNotCome(t *testing.T) {
	cfg := &fakeConfigVersions{wait: true}
	env := newWSEnv(t, func(o *WSOptions, _ *httptest.Server) { o.Config = cfg })
	start := time.Now()
	_, hello := env.connect(t, env.tokenFor(t, "alice"))
	if hello["configVersion"] != float64(0) {
		t.Errorf("configVersion = %v, want 0 when the config is not known", hello["configVersion"])
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("hello took %v, want about %v", d, configVersionTimeout)
	}
}
