package handler

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Frame types and error codes of the WebSocket protocol (see openapi/realtime-ws.md).
const (
	frameTypePing  = "ping"
	frameTypePong  = "pong"
	frameTypeErr   = "error"
	frameTypeHello = "hello"

	// Types the protocol defines but this service does not handle yet.
	frameTypeMessageSend      = "message.send"
	frameTypeConversationRead = "conversation.read"

	codeBadFrame = "bad_frame"

	// maxFrameIDLen is the longest request id a client may send.
	maxFrameIDLen = 64
)

// clientFrame is the envelope of every frame a client sends: {"type": "...", "id": "...", "data": {...}}.
type clientFrame struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

// serverFrame is the envelope of every frame the server sends.
type serverFrame struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type pongData struct {
	ReplyTo *string `json:"replyTo"`
}

type errorData struct {
	Code         string  `json:"code"`
	Message      string  `json:"message"`
	ReplyTo      *string `json:"replyTo"`
	RetryAfterMs *int    `json:"retryAfterMs,omitempty"`
}

// frameError is a client frame the server cannot accept. Its code is always bad_frame.
type frameError struct {
	message string
	replyTo *string // the frame's id, when it could be read
}

func (e *frameError) Error() string { return e.message }

// parseFrame reads one client frame. It checks the envelope only: a JSON object with a type,
// an optional id of at most 64 characters and an optional data object. What the data must contain
// is checked by the code that handles the type.
func parseFrame(raw []byte) (clientFrame, *frameError) {
	var f clientFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field == "id" {
			return clientFrame{}, &frameError{message: "The frame id must be a string"}
		}
		return clientFrame{}, &frameError{message: "The frame is not a JSON object"}
	}

	var replyTo *string
	if f.ID != "" {
		replyTo = &f.ID
	}
	if len(f.ID) > maxFrameIDLen {
		return clientFrame{}, &frameError{message: "The frame id is longer than 64 characters"}
	}
	if f.Type == "" {
		return clientFrame{}, &frameError{message: "The frame has no type", replyTo: replyTo}
	}
	if len(f.Data) > 0 && !bytes.HasPrefix(bytes.TrimSpace(f.Data), []byte("{")) {
		return clientFrame{}, &frameError{message: "The frame data must be a JSON object", replyTo: replyTo}
	}
	return f, nil
}

// idPointer returns the frame id for a reply, or nil when the client sent none.
func (f clientFrame) idPointer() *string {
	if f.ID == "" {
		return nil
	}
	return &f.ID
}
