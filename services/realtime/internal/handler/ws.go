package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/auth"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/frame"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/hub"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/social"
)

// TokenVerifier checks the player token of a WebSocket request.
// It returns an *httpx.Error (401) when the token is not acceptable.
type TokenVerifier interface {
	Verify(token string) (auth.Player, error)
}

// PlayerResolver turns a verified token into social's own player ID.
// It fails with social.ErrUnavailable when social cannot answer.
type PlayerResolver interface {
	Player(ctx context.Context, token, gameID string) (social.Player, error)
}

// WSOptions configure the WebSocket endpoint.
type WSOptions struct {
	// Verifier checks the token in ?token=. Required.
	Verifier TokenVerifier
	// Players looks up social's player ID for the token. Required.
	Players PlayerResolver
	// Origins are the pages allowed to open a socket, exactly scheme://host:port. A request
	// whose Origin header is not listed is refused with 403 before the upgrade. Requests
	// without an Origin header (not from a browser) are not affected.
	Origins []string
	// Config tells the version of the game's published config for the hello frame. Optional: nil, or a
	// game whose config is not known, gives configVersion 0.
	Config ConfigVersions
	// Presence follows who is online (the grace period, friends). Optional: nil means nobody is told
	// anything, and hello lists no online friends.
	Presence Presence
	// Hub holds the open connections. Pass the one the event consumer sends to; nil makes a new one.
	Hub *hub.Hub
	// Shutdown is cancelled when the service stops; every open socket is then closed with 1001.
	// Nil means never.
	Shutdown context.Context
	// PingInterval is how often the server pings an idle connection (default 25 s).
	PingInterval time.Duration
	// PongTimeout is how long the server waits for the answer before dropping the connection (default 60 s).
	PongTimeout time.Duration
	// SendBuffer is how many frames may wait for one slow client before it is dropped (default 64).
	SendBuffer int
	// WriteTimeout is how long writing one frame may take (default 10 s).
	WriteTimeout time.Duration
}

func (o WSOptions) withDefaults() WSOptions {
	if o.Shutdown == nil {
		o.Shutdown = context.Background()
	}
	return o
}

// ConfigVersions gives the version of a game's published config, 0 when it is not known.
// gameconfig.Cache implements it. It must not fail: hello is sent either way.
type ConfigVersions interface {
	Version(ctx context.Context, gameID string) int
}

// Presence is what the WebSocket endpoint needs from the presence tracker. presence.Tracker implements it.
type Presence interface {
	// Connected is called after the player's connection was added to the hub.
	Connected(gameID, playerID string)
	// Disconnected is called after the player's last connection was removed from the hub.
	Disconnected(gameID, playerID string)
	// OnlineFriends returns the IDs of the player's friends who are online now.
	OnlineFriends(ctx context.Context, gameID, playerID string) ([]string, error)
}

// onlineFriendsTimeout limits how long a new connection waits for its friends to be looked up. After
// it hello lists nobody and the connection goes on.
const onlineFriendsTimeout = 2 * time.Second

// configVersionTimeout limits how long a new connection waits for the config version (the first
// connection of a game, when the cache is empty). After it hello says 0 and the connection goes on.
const configVersionTimeout = time.Second

// resolveTimeout limits the call to social that happens before the upgrade.
const resolveTimeout = 3 * time.Second

// helloData is the data of the first frame every connection gets.
type helloData struct {
	PlayerID      string   `json:"playerId"`
	ConfigVersion int      `json:"configVersion"`
	OnlineFriends []string `json:"onlineFriends"`
}

// ws upgrades GET /api/v1/ws to a WebSocket. The token and the origin are checked before the
// upgrade, so a client that fails them gets a normal HTTP error and never holds a socket.
// The URL carries the token, so nothing here logs the URL or the query string.
func (h *Handler) ws(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	player, err := h.wsOpts.Verifier.Verify(token)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="realtime"`)
		httpx.WriteErr(w, err)
		return
	}
	// Count the connection before the upgrade, so Wait cannot miss one that is being set up.
	if h.wsOpts.Shutdown.Err() != nil || !h.trackConn() {
		httpx.WriteError(w, http.StatusServiceUnavailable, "shutting_down", "The service is shutting down")
		return
	}
	defer h.conns.Done()

	// Social's player ID is what conversations, members and events use. Asking before the
	// upgrade means a failure is a plain HTTP error the SDK can retry with backoff.
	ctx, cancel := context.WithTimeout(r.Context(), resolveTimeout)
	sp, err := h.wsOpts.Players.Player(ctx, token, player.GameID)
	cancel()
	if err != nil {
		if errors.Is(err, social.ErrUnavailable) {
			h.log.Warn("cannot resolve the player in social", "game_id", player.GameID, "error", err)
		} else {
			h.log.Error("resolve the player in social", "game_id", player.GameID, "error", err)
		}
		httpx.WriteError(w, http.StatusServiceUnavailable, "social_unavailable", "Cannot reach the social service; try again shortly")
		return
	}

	// Accept checks the Origin again (scheme, host and port) and answers 403 itself when it does not match.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.wsOpts.Origins})
	if err != nil {
		h.log.Debug("websocket upgrade refused", "error", err)
		return
	}

	log := h.log.With("game_id", player.GameID, "player", player.ExternalID, "player_id", sp.ID)
	conn := hub.NewConn(ws, hub.Identity{
		GameID:     player.GameID,
		PlayerID:   sp.ID,
		ExternalID: player.ExternalID,
		Name:       sp.DisplayName,
	}, hub.ConnOptions{
		SendBuffer:   h.wsOpts.SendBuffer,
		ExpiresAt:    player.ExpiresAt,
		PingInterval: h.wsOpts.PingInterval,
		PongTimeout:  h.wsOpts.PongTimeout,
		WriteTimeout: h.wsOpts.WriteTimeout,
		Shutdown:     h.wsOpts.Shutdown,
		Log:          log,
	})

	// config_version is the game's published config version, 0 when it is not known (nothing published yet,
	// or Platform did not answer in time). It is read before the connection is registered, so a config.updated
	// can never reach the connection ahead of its hello. One that is sent in between is not missed for good:
	// hello then names the older version, and the SDK sees that it differs from the one it has.
	configVersion := 0
	if h.wsOpts.Config != nil {
		cctx, cancel := context.WithTimeout(r.Context(), configVersionTimeout)
		configVersion = h.wsOpts.Config.Version(cctx, player.GameID)
		cancel()
	}

	h.hub.Register(conn)
	defer func() {
		// The last tab closing starts the grace period; friends hear "offline" only if nobody comes back.
		if h.hub.Unregister(conn) && h.wsOpts.Presence != nil {
			h.wsOpts.Presence.Disconnected(player.GameID, sp.ID)
		}
	}()
	if h.wsOpts.Presence != nil {
		// A player who comes back within the grace period was never offline: nobody is told.
		h.wsOpts.Presence.Connected(player.GameID, sp.ID)
	}
	log.Info("websocket connected")

	h.enqueue(conn, serverFrame{Type: frameTypeHello, Data: helloData{PlayerID: sp.ID, ConfigVersion: configVersion, OnlineFriends: h.onlineFriends(r.Context(), log, player.GameID, sp.ID)}})

	conn.Serve(r.Context(), h.handleFrame)

	code, reason := conn.CloseInfo()
	log.Info("websocket disconnected", "code", int(code), "reason", reason)
}

// onlineFriends lists the friends of the player who are online, for hello. It never fails: when the
// friends cannot be looked up the list is empty (and the SDK learns about them from later frames).
func (h *Handler) onlineFriends(ctx context.Context, log *slog.Logger, gameID, playerID string) []string {
	if h.wsOpts.Presence == nil {
		return []string{}
	}
	ctx, cancel := context.WithTimeout(ctx, onlineFriendsTimeout)
	defer cancel()
	friends, err := h.wsOpts.Presence.OnlineFriends(ctx, gameID, playerID)
	if err != nil {
		log.Warn("cannot list the online friends for hello", "error", err)
		return []string{}
	}
	if friends == nil {
		return []string{}
	}
	return friends
}

// handleFrame answers one client frame. A frame the server cannot accept gets an error frame
// and the connection stays open.
func (h *Handler) handleFrame(c *hub.Conn, typ websocket.MessageType, data []byte) {
	if typ != websocket.MessageText {
		h.enqueueError(c, codeBadFrame, "Frames must be text (JSON)", nil)
		return
	}
	f, ferr := parseFrame(data)
	if ferr != nil {
		h.enqueueError(c, codeBadFrame, ferr.message, ferr.replyTo)
		return
	}

	switch f.Type {
	case frameTypePing:
		h.enqueue(c, serverFrame{Type: frameTypePong, Data: pongData{ReplyTo: f.idPointer()}})
	case frameTypeMessageSend:
		h.messageSend(c, f)
	case frameTypeConversationRead:
		h.conversationRead(c, f)
	default:
		h.enqueueError(c, codeBadFrame, "Unknown frame type", f.idPointer())
	}
}

func (h *Handler) enqueueError(c *hub.Conn, code, message string, replyTo *string) {
	h.enqueue(c, serverFrame{Type: frameTypeErr, Data: errorData{Code: code, Message: message, ReplyTo: replyTo}})
}

// enqueue puts the event envelope around one frame, for the connection's game, and queues it for
// the connection's writer. It never blocks.
func (h *Handler) enqueue(c *hub.Conn, f serverFrame) {
	b, err := frame.Encode(c.Identity().GameID, f.Type, f.At, f.Data)
	if err != nil {
		// Only possible with a frame type that cannot be encoded: a bug, not a client error.
		h.log.Error("encode frame", "type", f.Type, "error", err)
		return
	}
	c.Enqueue(b)
}

// Hub returns the hub of open connections, for the parts of the service that push frames
// (chat, presence, events).
func (h *Handler) Hub() *hub.Hub { return h.hub }

// trackConn registers a connection that is about to be set up. It reports false once Wait
// has started, because from then on the service is stopping.
func (h *Handler) trackConn() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	h.conns.Add(1)
	return true
}

// Wait blocks until every open connection has finished, or until timeout. Call it once, after
// the server stopped, so connections closed with 1001 get the time to send their close frame.
// New connections are refused from the moment Wait starts.
func (h *Handler) Wait(timeout time.Duration) {
	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()

	done := make(chan struct{})
	go func() {
		h.conns.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		h.log.Warn("websocket connections still open at exit")
	}
}
