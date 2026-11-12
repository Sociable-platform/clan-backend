package hub

import "sync"

// Hub knows every open connection, grouped by game and player. A player can have several
// (one per tab). Games never see each other's connections: every lookup starts with the game ID.
type Hub struct {
	mu    sync.RWMutex                  // guards conns only; never held while a frame is queued
	conns map[string]map[string][]*Conn // game ID -> player ID -> tabs
}

// New returns an empty Hub.
func New() *Hub {
	return &Hub{conns: make(map[string]map[string][]*Conn)}
}

// Register adds a connection. It reports whether it is the player's first open connection
// (the player just came online).
func (h *Hub) Register(c *Conn) (first bool) {
	id := c.Identity()
	h.mu.Lock()
	defer h.mu.Unlock()
	players := h.conns[id.GameID]
	if players == nil {
		players = make(map[string][]*Conn)
		h.conns[id.GameID] = players
	}
	first = len(players[id.PlayerID]) == 0
	players[id.PlayerID] = append(players[id.PlayerID], c)
	return first
}

// Unregister removes a connection. It reports whether it was the player's last one
// (the player just went offline). Removing a connection that is not registered does nothing.
func (h *Hub) Unregister(c *Conn) (last bool) {
	id := c.Identity()
	h.mu.Lock()
	defer h.mu.Unlock()
	tabs := h.conns[id.GameID][id.PlayerID]
	for i, t := range tabs {
		if t != c {
			continue
		}
		// Build a new slice: a fan-out in progress may still hold the old one.
		rest := make([]*Conn, 0, len(tabs)-1)
		rest = append(rest, tabs[:i]...)
		rest = append(rest, tabs[i+1:]...)
		if len(rest) > 0 {
			h.conns[id.GameID][id.PlayerID] = rest
			return false
		}
		delete(h.conns[id.GameID], id.PlayerID)
		if len(h.conns[id.GameID]) == 0 {
			delete(h.conns, id.GameID)
		}
		return true
	}
	return false
}

// SendToPlayers queues frame on every open connection of every listed player of the game.
// The frame is encoded once by the caller and shared. It never blocks: a connection whose
// queue is full is closed with 4002 (see Conn.Enqueue) and the others still get the frame.
// Players that are not connected are skipped.
func (h *Hub) SendToPlayers(gameID string, playerIDs []string, frame []byte) {
	// Collect the targets under the read lock, then release it before touching any queue.
	var targets []*Conn
	h.mu.RLock()
	players := h.conns[gameID]
	seen := make(map[string]struct{}, len(playerIDs)) // a player listed twice gets the frame once
	for _, pid := range playerIDs {
		if _, dup := seen[pid]; dup {
			continue
		}
		seen[pid] = struct{}{}
		targets = append(targets, players[pid]...)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		c.Enqueue(frame)
	}
}

// SendToGame queues frame on every open connection of the game, whoever the player is. The frame is
// encoded once by the caller and shared. It never blocks, like SendToPlayers. Other games get nothing.
func (h *Hub) SendToGame(gameID string, frame []byte) {
	var targets []*Conn
	h.mu.RLock()
	for _, tabs := range h.conns[gameID] {
		targets = append(targets, tabs...)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		c.Enqueue(frame)
	}
}

// Online reports whether the player has at least one open connection in the game.
func (h *Hub) Online(gameID, playerID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns[gameID][playerID]) > 0
}

// Len returns the number of open connections of all games.
func (h *Hub) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, players := range h.conns {
		for _, tabs := range players {
			n += len(tabs)
		}
	}
	return n
}
