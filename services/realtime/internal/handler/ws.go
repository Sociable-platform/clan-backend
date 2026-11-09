package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"gitlab.com/clan-platform/clan-backend/pkg/httpx"
	"gitlab.com/clan-platform/clan-backend/services/realtime/internal/auth"
)

const (
	// maxFrameBytes is the biggest frame a client may send; a larger one closes the connection (1009).
	maxFrameBytes = 8 << 10

	// closeTokenExpired tells the SDK to get a new token and reconnect at once.
	closeTokenExpired websocket.StatusCode = 4001

	writeTimeout = 10 * time.Second

	defaultPingInterval = 30 * time.Second
	defaultPongTimeout  = 60 * time.Second
)

// TokenVerifier checks the player token of a WebSocket request.
// It returns an *httpx.Error (401) when the token is not acceptable.
type TokenVerifier interface {
	Verify(token string) (auth.Player, error)
}

// WSOptions configure the WebSocket endpoint.
type WSOptions struct {
	// Verifier checks the token in ?token=. Required.
	Verifier TokenVerifier
	// Origins are the pages allowed to open a socket, exactly scheme://host:port. A request
	// whose Origin header is not listed is refused with 403 before the upgrade. Requests
	// without an Origin header (not from a browser) are not affected.
	Origins []string
	// Shutdown is cancelled when the service stops; every open socket is then closed with 1001.
	// Nil means never.
	Shutdown context.Context
	// PingInterval is how often the server pings an idle connection (default 30 s).
	PingInterval time.Duration
	// PongTimeout is how long the server waits for the answer before dropping the connection (default 60 s).
	PongTimeout time.Duration
}

func (o WSOptions) withDefaults() WSOptions {
	if o.Shutdown == nil {
		o.Shutdown = context.Background()
	}
	if o.PingInterval <= 0 {
		o.PingInterval = defaultPingInterval
	}
	if o.PongTimeout <= 0 {
		o.PongTimeout = defaultPongTimeout
	}
	return o
}

// ws upgrades GET /api/v1/ws to a WebSocket. The token and the origin are checked before the
// upgrade, so a client that fails them gets a normal HTTP error and never holds a socket.
// The URL carries the token, so nothing here logs the URL or the query string.
func (h *Handler) ws(w http.ResponseWriter, r *http.Request) {
	player, err := h.wsOpts.Verifier.Verify(r.URL.Query().Get("token"))
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

	// Accept checks the Origin again (scheme, host and port) and answers 403 itself when it does not match.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.wsOpts.Origins})
	if err != nil {
		h.log.Debug("websocket upgrade refused", "error", err)
		return
	}
	h.serve(r.Context(), c, player)
}

// serve runs one connection until the client leaves, the token expires, the service stops
// or the client stops answering pings.
func (h *Handler) serve(ctx context.Context, c *websocket.Conn, p auth.Player) {
	defer c.CloseNow() // no-op after a clean close; frees the connection on every other path
	c.SetReadLimit(maxFrameBytes)

	ctx, cancel := context.WithCancel(ctx)
	var helpers sync.WaitGroup
	defer func() {
		cancel()
		helpers.Wait()
	}()
	helpers.Go(func() { h.closeWhenDue(ctx, c, p) })
	helpers.Go(func() { h.keepAlive(ctx, c) })

	log := h.log.With("game_id", p.GameID, "player", p.ExternalID)
	log.Info("websocket connected")
	defer func() { log.Info("websocket disconnected") }()

	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			if status := websocket.CloseStatus(err); status != -1 && status != websocket.StatusNormalClosure && status != websocket.StatusGoingAway {
				log.Info("websocket closed", "code", int(status))
			}
			return
		}
		if err := h.handleFrame(ctx, c, typ, data); err != nil {
			log.Warn("websocket write failed", "error", err)
			return
		}
	}
}

// closeWhenDue closes the connection when the service stops (1001) or the player's token
// expires (4001). Both are in the protocol, so the SDK knows what to do next.
func (h *Handler) closeWhenDue(ctx context.Context, c *websocket.Conn, p auth.Player) {
	expiry := time.NewTimer(time.Until(p.ExpiresAt))
	defer expiry.Stop()
	select {
	case <-ctx.Done():
	case <-h.wsOpts.Shutdown.Done():
		_ = c.Close(websocket.StatusGoingAway, "server shutting down")
	case <-expiry.C:
		_ = c.Close(closeTokenExpired, "token expired")
	}
}

// keepAlive pings the client and drops the connection when it does not answer in time.
// Browsers answer pings by themselves.
func (h *Handler) keepAlive(ctx context.Context, c *websocket.Conn) {
	ticker := time.NewTicker(h.wsOpts.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, h.wsOpts.PongTimeout)
		err := c.Ping(pingCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				_ = c.CloseNow() // the peer is gone, a close frame would not arrive
			}
			return
		}
	}
}

// handleFrame answers one client frame. A frame the server cannot accept gets an error frame
// and the connection stays open; only a failed write ends the connection.
func (h *Handler) handleFrame(ctx context.Context, c *websocket.Conn, typ websocket.MessageType, data []byte) error {
	if typ != websocket.MessageText {
		return h.sendError(ctx, c, codeBadFrame, "Frames must be text (JSON)", nil)
	}
	f, ferr := parseFrame(data)
	if ferr != nil {
		return h.sendError(ctx, c, codeBadFrame, ferr.message, ferr.replyTo)
	}

	switch f.Type {
	case frameTypePing:
		return h.send(ctx, c, serverFrame{Type: frameTypePong, Data: pongData{ReplyTo: f.idPointer()}})
	case frameTypeMessageSend, frameTypeConversationRead:
		return h.sendError(ctx, c, codeBadFrame, "This frame type is not available yet", f.idPointer())
	default:
		return h.sendError(ctx, c, codeBadFrame, "Unknown frame type", f.idPointer())
	}
}

func (h *Handler) sendError(ctx context.Context, c *websocket.Conn, code, message string, replyTo *string) error {
	return h.send(ctx, c, serverFrame{Type: frameTypeErr, Data: errorData{Code: code, Message: message, ReplyTo: replyTo}})
}

// send writes one frame. Writes to one connection may come from several goroutines;
// the websocket library serialises them.
func (h *Handler) send(ctx context.Context, c *websocket.Conn, f serverFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode %s frame: %w", f.Type, err)
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("write %s frame: %w", f.Type, err)
	}
	return nil
}

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
