// Command e2e checks the realtime flow end to end against a running stack.
//
// It plays a developer and two players over the real HTTP APIs and real WebSockets:
//
//  1. the developer registers, creates a game and an API key and publishes a config (version 1);
//  2. two players get tokens from Platform and connect to Realtime, each receives hello;
//  3. player A asks B to be friends, B accepts, and both receive conversation.added;
//  4. A sends a direct message: A receives message.ack, A and B receive message.new;
//  5. the developer publishes a new config: both players receive config.updated.
//
// Every frame must arrive within 2 seconds of the action that causes it. The exit code is 0 when
// every step passes and 1 otherwise. Start the stack first (see README, "End-to-end test"):
//
//	docker compose up -d --build
//	go run ./tools/e2e
//
// The run leaves a developer, a game and two players in the development database; it is for
// local use and CI, not for production.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	// maxResponseBody caps what the script reads from one HTTP answer.
	maxResponseBody = 1 << 20
	// runLimit is the longest the whole run may take, so a hung service cannot hang CI.
	runLimit = 60 * time.Second
	// developerPassword is the password of the throwaway developer account.
	developerPassword = "e2e-password-123"
)

// templateConfig is a complete, valid widget config; see saveAndPublish.
//
//go:embed config.json
var templateConfig []byte

// config holds the addresses and the deadline of one run.
type config struct {
	platformURL string
	socialURL   string
	realtimeURL string
	deadline    time.Duration
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config{}
	flag.StringVar(&cfg.platformURL, "platform", envOr("E2E_PLATFORM_URL", "http://localhost:8081"), "base URL of the platform service")
	flag.StringVar(&cfg.socialURL, "social", envOr("E2E_SOCIAL_URL", "http://localhost:8082"), "base URL of the social service")
	flag.StringVar(&cfg.realtimeURL, "realtime", envOr("E2E_REALTIME_URL", "http://localhost:8083"), "base URL of the realtime service")
	flag.DurationVar(&cfg.deadline, "deadline", 2*time.Second, "longest a frame may take to arrive after its action")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, runLimit)
	defer cancel()

	return newScenario(cfg).run(ctx)
}

// scenario runs the steps in order and prints one line per step.
type scenario struct {
	cfg config
	api *apiClient
}

func newScenario(cfg config) *scenario {
	return &scenario{cfg: cfg, api: &apiClient{http: &http.Client{Timeout: 10 * time.Second}}}
}

// player is one test player: the game's token, the social ID and the open WebSocket.
type player struct {
	name     string
	token    string
	socialID string
	ws       *wsClient
}

func (s *scenario) run(ctx context.Context) error {
	if err := s.checkReady(ctx); err != nil {
		return err
	}

	game, err := s.setupGame(ctx)
	if err != nil {
		return err
	}

	a, err := s.newPlayer(ctx, game, "e2e-a", "Ali")
	if err != nil {
		return err
	}
	defer a.ws.close()
	b, err := s.newPlayer(ctx, game, "e2e-b", "Rashad")
	if err != nil {
		return err
	}
	defer b.ws.close()

	if err := s.checkHello(a, 1); err != nil {
		return err
	}
	if err := s.checkHello(b, 1); err != nil {
		return err
	}

	conversationID, err := s.becomeFriends(ctx, a, b)
	if err != nil {
		return err
	}
	if err := s.sendDirectMessage(ctx, a, b, conversationID); err != nil {
		return err
	}
	if err := s.publishConfig(ctx, game, 2, a, b); err != nil {
		return err
	}

	fmt.Println("PASS: all steps")
	return nil
}

// checkReady makes sure all three services answer /ready, so a stack that is still starting
// gives one clear message instead of a confusing failure later.
func (s *scenario) checkReady(ctx context.Context) error {
	for name, base := range map[string]string{"platform": s.cfg.platformURL, "social": s.cfg.socialURL, "realtime": s.cfg.realtimeURL} {
		if err := s.api.call(ctx, http.MethodGet, base+"/ready", nil, nil, nil, http.StatusOK); err != nil {
			return fmt.Errorf("%s is not ready (is the stack up? see README): %w", name, err)
		}
	}
	pass("services are ready", 0)
	return nil
}

// game is the throwaway game of one run.
type game struct {
	id             string
	apiKey         string
	developerToken string
}

func (g game) developerHeader() http.Header {
	return http.Header{"Authorization": {"Bearer " + g.developerToken}}
}

// setupGame creates a developer, a game and an API key, and publishes config version 1.
func (s *scenario) setupGame(ctx context.Context) (game, error) {
	p := s.cfg.platformURL
	stamp := time.Now().UnixNano()
	email := fmt.Sprintf("e2e-%d@example.com", stamp)

	reg := map[string]string{"email": email, "password": developerPassword, "name": "E2E Developer"}
	if err := s.api.call(ctx, http.MethodPost, p+"/api/v1/developers/register", nil, reg, nil, http.StatusCreated); err != nil {
		return game{}, fmt.Errorf("register developer: %w", err)
	}

	var login struct {
		Token string `json:"token"`
	}
	creds := map[string]string{"email": email, "password": developerPassword}
	if err := s.api.call(ctx, http.MethodPost, p+"/api/v1/developers/login", nil, creds, &login, http.StatusOK); err != nil {
		return game{}, fmt.Errorf("log in developer: %w", err)
	}
	g := game{developerToken: login.Token}

	var created struct {
		ID string `json:"id"`
	}
	body := map[string]string{"name": fmt.Sprintf("E2E game %d", stamp), "description": "created by tools/e2e"}
	if err := s.api.call(ctx, http.MethodPost, p+"/api/v1/games", g.developerHeader(), body, &created, http.StatusCreated); err != nil {
		return game{}, fmt.Errorf("create game: %w", err)
	}
	g.id = created.ID

	var key struct {
		Key string `json:"key"`
	}
	if err := s.api.call(ctx, http.MethodPost, p+"/api/v1/games/"+g.id+"/api-keys", g.developerHeader(), nil, &key, http.StatusCreated); err != nil {
		return game{}, fmt.Errorf("create API key: %w", err)
	}
	g.apiKey = key.Key

	if _, err := s.saveAndPublish(ctx, g, 500); err != nil {
		return game{}, err
	}
	pass("developer, game, API key and config v1", 0)
	return g, nil
}

// saveAndPublish saves the template config as the draft, with the given chat.maxMessageLength, and
// publishes it. It returns the published version. Platform needs a complete config, so the script
// carries one (config.json, a copy of a valid example) and changes only game_id and that one value.
func (s *scenario) saveAndPublish(ctx context.Context, g game, maxMessageLength int) (int, error) {
	base := s.cfg.platformURL + "/api/v1/games/" + g.id + "/widget"

	var draft map[string]any
	if err := json.Unmarshal(templateConfig, &draft); err != nil {
		return 0, fmt.Errorf("read config.json: %w", err)
	}
	chat, ok := draft["chat"].(map[string]any)
	if !ok {
		return 0, errors.New("config.json has no chat section")
	}
	chat["maxMessageLength"] = maxMessageLength
	draft["game_id"] = g.id

	if err := s.api.call(ctx, http.MethodPut, base+"/draft", g.developerHeader(), map[string]any{"config": draft}, nil, http.StatusOK); err != nil {
		return 0, fmt.Errorf("save draft: %w", err)
	}
	var version struct {
		Version int `json:"version"`
	}
	if err := s.api.call(ctx, http.MethodPost, base+"/publish", g.developerHeader(), nil, &version, http.StatusCreated); err != nil {
		return 0, fmt.Errorf("publish config: %w", err)
	}
	return version.Version, nil
}

// newPlayer gets a player token from Platform, creates the player in Social and connects to Realtime.
func (s *scenario) newPlayer(ctx context.Context, g game, externalID, displayName string) (*player, error) {
	var auth struct {
		Token string `json:"token"`
	}
	body := map[string]string{"externalPlayerId": externalID, "displayName": displayName}
	key := http.Header{"X-API-Key": {g.apiKey}}
	if err := s.api.call(ctx, http.MethodPost, s.cfg.platformURL+"/api/v1/players/auth", key, body, &auth, http.StatusOK); err != nil {
		return nil, fmt.Errorf("get token for %s: %w", displayName, err)
	}

	var me struct {
		ID string `json:"id"`
	}
	bearer := http.Header{"Authorization": {"Bearer " + auth.Token}}
	if err := s.api.call(ctx, http.MethodGet, s.cfg.socialURL+"/api/v1/players/me", bearer, nil, &me, http.StatusOK); err != nil {
		return nil, fmt.Errorf("create player %s in social: %w", displayName, err)
	}

	ws, err := dialWS(ctx, displayName, s.cfg.realtimeURL, auth.Token)
	if err != nil {
		return nil, err
	}
	return &player{name: displayName, token: auth.Token, socialID: me.ID, ws: ws}, nil
}

// checkHello reads the hello frame of a player and checks the player ID and the config version.
func (s *scenario) checkHello(p *player, wantVersion int) error {
	start := time.Now()
	f, err := p.ws.expect(start.Add(s.cfg.deadline), "hello", ofType("hello"))
	if err != nil {
		return err
	}
	var hello struct {
		PlayerID      string `json:"playerId"`
		ConfigVersion int    `json:"configVersion"`
	}
	if err := json.Unmarshal(f.Data, &hello); err != nil {
		return fmt.Errorf("%s: read hello: %w", p.name, err)
	}
	if hello.PlayerID != p.socialID {
		return fmt.Errorf("%s: hello playerId = %q, want %q", p.name, hello.PlayerID, p.socialID)
	}
	if hello.ConfigVersion != wantVersion {
		return fmt.Errorf("%s: hello configVersion = %d, want %d", p.name, hello.ConfigVersion, wantVersion)
	}
	pass(p.name+" connected and got hello", time.Since(start))
	return nil
}

// becomeFriends sends a request from a to b, accepts it and waits for the DM conversation on both
// sockets. It returns the conversation ID.
func (s *scenario) becomeFriends(ctx context.Context, a, b *player) (string, error) {
	start := time.Now()
	var request struct {
		ID string `json:"id"`
	}
	err := s.api.call(ctx, http.MethodPost, s.cfg.socialURL+"/api/v1/friends/requests",
		bearerHeader(a.token), map[string]string{"playerId": b.socialID}, &request, http.StatusCreated)
	if err != nil {
		return "", fmt.Errorf("send friend request: %w", err)
	}
	if _, err := b.ws.expect(start.Add(s.cfg.deadline), "friend_request notification", ofNotification("friend_request")); err != nil {
		return "", err
	}
	pass(b.name+" got the friend_request notification", time.Since(start))

	start = time.Now()
	err = s.api.call(ctx, http.MethodPost, s.cfg.socialURL+"/api/v1/friends/requests/"+request.ID+"/accept",
		bearerHeader(b.token), nil, nil, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("accept friend request: %w", err)
	}

	var ids [2]string
	for i, p := range []*player{a, b} {
		f, err := p.ws.expect(start.Add(s.cfg.deadline), "conversation.added", ofType("conversation.added"))
		if err != nil {
			return "", err
		}
		var added struct {
			Conversation struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			} `json:"conversation"`
		}
		if err := json.Unmarshal(f.Data, &added); err != nil {
			return "", fmt.Errorf("%s: read conversation.added: %w", p.name, err)
		}
		if added.Conversation.Kind != "dm" {
			return "", fmt.Errorf("%s: conversation kind = %q, want dm", p.name, added.Conversation.Kind)
		}
		ids[i] = added.Conversation.ID
	}
	if ids[0] == "" || ids[0] != ids[1] {
		return "", fmt.Errorf("the two players got different conversations: %q and %q", ids[0], ids[1])
	}
	pass("friends: both got conversation.added", time.Since(start))
	return ids[0], nil
}

// sendDirectMessage sends a message from a and checks the ack for a and message.new for both.
func (s *scenario) sendDirectMessage(ctx context.Context, a, b *player, conversationID string) error {
	const (
		frameID = "e2e-1"
		text    = "gg from the e2e test"
	)
	start := time.Now()
	send := map[string]any{
		"type": "message.send",
		"id":   frameID,
		"data": map[string]string{"conversationId": conversationID, "body": text},
	}
	if err := a.ws.send(ctx, send); err != nil {
		return err
	}

	ack, err := a.ws.expect(start.Add(s.cfg.deadline), "message.ack", ofType("message.ack"))
	if err != nil {
		return err
	}
	var ackData struct {
		ReplyTo   string `json:"replyTo"`
		MessageID string `json:"messageId"`
	}
	if err := json.Unmarshal(ack.Data, &ackData); err != nil {
		return fmt.Errorf("%s: read message.ack: %w", a.name, err)
	}
	if ackData.ReplyTo != frameID || ackData.MessageID == "" {
		return fmt.Errorf("%s: message.ack = %+v, want replyTo %q and a message ID", a.name, ackData, frameID)
	}

	for _, p := range []*player{b, a} {
		f, err := p.ws.expect(start.Add(s.cfg.deadline), "message.new", ofType("message.new"))
		if err != nil {
			return err
		}
		var msg struct {
			ConversationID string `json:"conversationId"`
			Message        struct {
				ID       string `json:"id"`
				SenderID string `json:"senderId"`
				Body     string `json:"body"`
			} `json:"message"`
		}
		if err := json.Unmarshal(f.Data, &msg); err != nil {
			return fmt.Errorf("%s: read message.new: %w", p.name, err)
		}
		if msg.ConversationID != conversationID || msg.Message.Body != text ||
			msg.Message.SenderID != a.socialID || msg.Message.ID != ackData.MessageID {
			return fmt.Errorf("%s: message.new = %+v, does not match the message that was sent", p.name, msg)
		}
	}
	pass("DM: ack for the sender, message.new for both", time.Since(start))
	return nil
}

// publishConfig publishes a new config and checks that every player gets config.updated.
func (s *scenario) publishConfig(ctx context.Context, g game, wantVersion int, players ...*player) error {
	start := time.Now()
	version, err := s.saveAndPublish(ctx, g, 400)
	if err != nil {
		return err
	}
	if version != wantVersion {
		return fmt.Errorf("published version = %d, want %d", version, wantVersion)
	}
	for _, p := range players {
		f, err := p.ws.expect(start.Add(s.cfg.deadline), "config.updated", ofType("config.updated"))
		if err != nil {
			return err
		}
		var updated struct {
			Version int `json:"version"`
		}
		if err := json.Unmarshal(f.Data, &updated); err != nil {
			return fmt.Errorf("%s: read config.updated: %w", p.name, err)
		}
		if updated.Version != wantVersion {
			return fmt.Errorf("%s: config.updated version = %d, want %d", p.name, updated.Version, wantVersion)
		}
	}
	pass(fmt.Sprintf("publish: config.updated v%d for both", wantVersion), time.Since(start))
	return nil
}

// pass prints one line for a finished step; elapsed 0 means "not timed".
func pass(step string, elapsed time.Duration) {
	if elapsed <= 0 {
		fmt.Printf("PASS  %s\n", step)
		return
	}
	fmt.Printf("PASS  %s (%d ms)\n", step, elapsed.Milliseconds())
}

func bearerHeader(token string) http.Header {
	return http.Header{"Authorization": {"Bearer " + token}}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// apiClient makes the JSON calls to the three services.
type apiClient struct {
	http *http.Client
}

// call sends one request and decodes the JSON answer into out (when not nil). Any status other
// than want is an error that carries the service's error code.
func (c *apiClient) call(ctx context.Context, method, rawURL string, header http.Header, in, out any, want int) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, req.URL.Path, withoutURL(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("%s %s: read answer: %w", method, req.URL.Path, err)
	}

	if resp.StatusCode != want {
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e) // an answer without the error shape just has no code
		return fmt.Errorf("%s %s: status %d (code %q), want %d", method, req.URL.Path, resp.StatusCode, e.Error.Code, want)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: decode answer: %w", method, req.URL.Path, err)
		}
	}
	return nil
}

// withoutURL drops the URL from a *url.Error: WebSocket URLs carry the player token.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// frame is a frame the server sends: the envelope of a queue event.
type frame struct {
	EventID    string          `json:"event_id"`
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurred_at"`
	GameID     string          `json:"game_id"`
	Data       json.RawMessage `json:"data"`
}

// ofType matches frames of one type.
func ofType(t string) func(frame) bool {
	return func(f frame) bool { return f.Type == t }
}

// ofNotification matches a notification frame of one kind.
func ofNotification(kind string) func(frame) bool {
	return func(f frame) bool {
		if f.Type != "notification" {
			return false
		}
		var n struct {
			Kind string `json:"kind"`
		}
		return json.Unmarshal(f.Data, &n) == nil && n.Kind == kind
	}
}

// wsClient is one WebSocket connection to realtime. A goroutine reads the frames into a channel;
// expect, called from one goroutine only, takes them out and keeps the ones it skipped.
type wsClient struct {
	name    string
	conn    *websocket.Conn
	frames  chan frame
	cancel  context.CancelFunc
	pending []frame
	readErr error // set before frames is closed
}

// dialWS opens the connection with the player token and starts the reader.
func dialWS(ctx context.Context, name, baseURL, token string) (*wsClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("realtime URL: %w", err)
	}
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1) // http -> ws, https -> wss
	u.Path = "/api/v1/ws"
	u.RawQuery = url.Values{"token": {token}}.Encode()

	conn, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s to realtime: %w", name, withoutURL(err))
	}
	return newWSClient(name, conn), nil
}

// newWSClient wraps an open connection and starts its reader.
func newWSClient(name string, conn *websocket.Conn) *wsClient {
	ctx, cancel := context.WithCancel(context.Background())
	c := &wsClient{name: name, conn: conn, frames: make(chan frame, 64), cancel: cancel}
	go c.readLoop(ctx)
	return c
}

// readLoop is the only goroutine that reads the connection. It ends when the connection fails or
// close is called, and then closes frames.
func (c *wsClient) readLoop(ctx context.Context) {
	defer close(c.frames)
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			c.readErr = err
			return
		}
		var f frame
		if err := json.Unmarshal(data, &f); err != nil {
			c.readErr = fmt.Errorf("read frame: %w", err)
			return
		}
		select {
		case c.frames <- f:
		case <-ctx.Done():
			c.readErr = ctx.Err()
			return
		}
	}
}

// send writes one client frame.
func (c *wsClient) send(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("%s: send frame: %w", c.name, err)
	}
	return nil
}

// expect returns the first frame that match accepts, or an error when none arrives before deadline
// or the connection ends. Frames that do not match stay for later calls.
func (c *wsClient) expect(deadline time.Time, desc string, match func(frame) bool) (frame, error) {
	for i, f := range c.pending {
		if match(f) {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return f, nil
		}
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case f, ok := <-c.frames:
			if !ok {
				return frame{}, fmt.Errorf("%s: connection ended while waiting for %s: %w", c.name, desc, c.readErr)
			}
			if match(f) {
				return f, nil
			}
			c.pending = append(c.pending, f)
		case <-timer.C:
			return frame{}, fmt.Errorf("%s: no %s arrived in time", c.name, desc)
		}
	}
}

// close stops the reader and closes the connection.
func (c *wsClient) close() {
	c.cancel()
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}
