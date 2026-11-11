// Package hub keeps the open WebSocket connections of the realtime service and delivers
// frames to them.
//
// Each connection (Conn) has one bounded send queue. Nothing but that connection's writer
// goroutine ever writes to its socket, and nothing ever waits for a slow client: a full
// queue closes the connection with 4002 instead of blocking the sender.
package hub

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Close codes the protocol defines (see openapi/realtime-ws.md).
const (
	CloseTokenExpired websocket.StatusCode = 4001                            // the player token passed its exp
	CloseSlowClient   websocket.StatusCode = 4002                            // the send queue is full
	ClosePolicy       websocket.StatusCode = websocket.StatusPolicyViolation // 1008: the client broke a rule (too many rate-limited messages)
)

// flushTimeout is how long a connection that is closing with its queue flushed may take to write it.
const flushTimeout = 2 * time.Second

// Defaults for ConnOptions, from the design doc.
const (
	DefaultSendBuffer   = 64
	DefaultPingInterval = 25 * time.Second
	DefaultPongTimeout  = 60 * time.Second
	DefaultWriteTimeout = 10 * time.Second
	DefaultReadLimit    = 8 << 10
)

// Identity says who is on the other end of a connection. It comes from the verified token
// and from social, never from anything the client sends in a frame.
type Identity struct {
	GameID     string // the game the player belongs to
	PlayerID   string // social's own ID for the player (a UUID)
	ExternalID string // the game's own player ID, e.g. "u_42"
	Name       string // display name
}

// ConnOptions tune a connection. Zero values use the defaults above.
type ConnOptions struct {
	SendBuffer   int           // frames the send queue holds
	ExpiresAt    time.Time     // the token's exp; the connection closes with 4001 then. Zero means never.
	PingInterval time.Duration // how often an idle connection is pinged
	PongTimeout  time.Duration // how long the pong may take before the connection is dropped
	WriteTimeout time.Duration // how long one frame may take to write
	ReadLimit    int64         // the biggest frame a client may send; larger closes with 1009
	Shutdown     context.Context
	Log          *slog.Logger
}

func (o ConnOptions) withDefaults() ConnOptions {
	if o.SendBuffer <= 0 {
		o.SendBuffer = DefaultSendBuffer
	}
	if o.PingInterval <= 0 {
		o.PingInterval = DefaultPingInterval
	}
	if o.PongTimeout <= 0 {
		o.PongTimeout = DefaultPongTimeout
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = DefaultWriteTimeout
	}
	if o.ReadLimit <= 0 {
		o.ReadLimit = DefaultReadLimit
	}
	if o.Shutdown == nil {
		o.Shutdown = context.Background()
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return o
}

// FrameHandler is called by the reader for every frame the client sends. It runs on the
// reader goroutine, so one connection's frames are handled in order. It must not block on
// anything slow; answer with Conn.Enqueue.
type FrameHandler func(c *Conn, typ websocket.MessageType, data []byte)

// Conn is one open WebSocket (one browser tab).
type Conn struct {
	ws   *websocket.Conn
	id   Identity
	opts ConnOptions

	send      chan []byte   // frames waiting for the writer; never closed, so a late Enqueue cannot panic
	done      chan struct{} // closed by Close; every goroutine of the connection stops on it
	closeOnce sync.Once
	code      websocket.StatusCode // set once, before done is closed
	reason    string
	flush     bool // set once, before done is closed: write the queued frames before the close frame
	dead      bool // set once, before done is closed: the client is gone, so the writer must not try a close handshake
}

// NewConn wraps an accepted WebSocket. Call Serve to run it.
func NewConn(ws *websocket.Conn, id Identity, opts ConnOptions) *Conn {
	opts = opts.withDefaults()
	return &Conn{
		ws:   ws,
		id:   id,
		opts: opts,
		send: make(chan []byte, opts.SendBuffer),
		done: make(chan struct{}),
	}
}

// Identity returns who the connection belongs to.
func (c *Conn) Identity() Identity { return c.id }

// Enqueue queues an already encoded frame for the writer. It never blocks: when the queue is
// full the client is too slow, so the connection is closed with 4002 and the frame is dropped.
// Safe to call from any goroutine, including after the connection closed (the frame is then dropped).
func (c *Conn) Enqueue(frame []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.send <- frame:
	default:
		c.Close(CloseSlowClient, "slow client")
	}
}

// Close asks the connection to close with the given code and reason. It never blocks and only
// the first call counts: the writer sends the close frame, and Serve returns when the client answered
// or gave up. Safe to call from any goroutine, any number of times.
func (c *Conn) Close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.code, c.reason = code, reason
		close(c.done)
	})
}

// closeDead is Close for a client that stopped answering. It also tells the writer not to send a
// close frame and wait up to 5 s for a reply that will never come: the caller closes the socket at
// once instead (CloseNow), but only when closeDead reports true. It reports false when another close
// came first; that close then ends the connection politely.
func (c *Conn) closeDead(code websocket.StatusCode, reason string) (won bool) {
	c.closeOnce.Do(func() {
		c.code, c.reason, c.dead = code, reason, true
		won = true
		close(c.done)
	})
	return won
}

// CloseAfterFlush is Close, but the writer first sends the frames that are already queued (for at most
// 2 seconds in all), so the client reads the last error frame before the close frame. Only the first
// call to Close or CloseAfterFlush counts.
func (c *Conn) CloseAfterFlush(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.code, c.reason, c.flush = code, reason, true
		close(c.done)
	})
}

// Done is closed once the connection was asked to close.
func (c *Conn) Done() <-chan struct{} { return c.done }

// CloseInfo returns the code and reason the connection was closed with. Only meaningful after Done is closed.
func (c *Conn) CloseInfo() (websocket.StatusCode, string) {
	select {
	case <-c.done:
		return c.code, c.reason
	default:
		return 0, ""
	}
}

// Serve runs the connection until it ends: the calling goroutine is the reader, and a writer
// and a keepalive goroutine are started and stopped with it. It returns after all three finished.
// ctx cancels the reader; it must not be cancelled to close a connection politely, use Close.
func (c *Conn) Serve(ctx context.Context, handle FrameHandler) {
	c.ws.SetReadLimit(c.opts.ReadLimit)

	var helpers sync.WaitGroup
	helpers.Go(c.writeLoop)
	helpers.Go(c.pingLoop)

	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			break
		}
		handle(c, typ, data)
	}
	// The client left, the writer closed the socket, or the frame was too big: wake the others.
	c.Close(websocket.StatusNormalClosure, "")
	helpers.Wait()
}

// writeLoop is the only goroutine that writes to the socket. It also watches the clock and the
// service shutdown, because both end in a close frame that only the writer may send.
func (c *Conn) writeLoop() {
	var expired <-chan time.Time
	if !c.opts.ExpiresAt.IsZero() {
		timer := time.NewTimer(time.Until(c.opts.ExpiresAt))
		defer timer.Stop()
		expired = timer.C
	}

	for {
		select {
		case frame := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), c.opts.WriteTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				// The client does not take frames any more; a close frame would not get through either.
				c.Close(websocket.StatusGoingAway, "write failed")
				_ = c.ws.CloseNow()
				return
			}
		case <-expired:
			c.Close(CloseTokenExpired, "token expired")
		case <-c.opts.Shutdown.Done():
			c.Close(websocket.StatusGoingAway, "server shutting down")
		case <-c.done:
			if c.dead {
				return // the client is gone; whoever marked it dead closes the socket
			}
			if c.flush {
				c.flushQueue()
			}
			// Sends the close frame and waits for the client's answer (at most 5 s); the reader
			// is still running, so it sees that answer and ends.
			_ = c.ws.Close(c.code, c.reason)
			return
		}
	}
}

// flushQueue writes the frames that are waiting, then returns, whether or not the client took them all.
func (c *Conn) flushQueue() {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	for {
		select {
		case frame := <-c.send:
			if err := c.ws.Write(ctx, websocket.MessageText, frame); err != nil {
				return
			}
		default:
			return
		}
	}
}

// pingLoop pings the client and drops it when no pong comes in time. It is its own goroutine
// because Ping waits for the pong, and the writer must never wait on a client.
func (c *Conn) pingLoop() {
	ticker := time.NewTicker(c.opts.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), c.opts.PongTimeout)
		err := c.ws.Ping(ctx)
		cancel()
		if err != nil {
			select {
			case <-c.done: // closing anyway; the failed ping is a result of that
			default:
				// The writer must not start the close handshake: it would wait 5 s for the reply, and
				// CloseNow would only wait behind it. Either this close wins and the socket is closed at
				// once, or another close came first and ends the connection politely.
				if c.closeDead(websocket.StatusGoingAway, "no pong") {
					_ = c.ws.CloseNow() // the client is gone, a close frame would not arrive
				}
			}
			return
		}
	}
}
