# Realtime WebSocket protocol

The live half of the realtime service: new messages, presence, notifications and live re-skin all arrive on one
WebSocket. The two REST reads (conversation list and history) are in [`realtime.yaml`](realtime.yaml).

- **Status:** contract for the SDK. Built so far: the endpoint with the token and origin checks, the `hello` frame, `ping`/`pong`,
  the 8 KB limit, `bad_frame` errors, the server's own keepalive ping (every 25 s), the close codes `1001`, `1009`, `4001` and `4002`,
  and the friend frames: `conversation.added`, `conversation.removed` and the `friend_request` and `friend_accepted` notifications.
  In `hello`, `configVersion` is always `0` ("not known yet") and `onlineFriends` is always `[]` until the config cache and friendships exist.
  Not built yet: `message.send`, `conversation.read` (both answer `error bad_frame` "not available yet"), presence, the clan notifications,
  clan conversations, the `config.updated` frame, the 5-connections-per-player limit, and `1008`/`4003`.
- **Endpoint:** `GET /api/v1/ws?token=<player token>` on the realtime service (`ws://localhost:8083/api/v1/ws` locally).
- **Format:** every frame is one JSON object in a WebSocket **text** frame. Binary frames are a `bad_frame` error.
- **Names:** frame types are lowercase with dots (`message.new`), JSON fields are camelCase, error codes are `snake_case`.
- **IDs:** player and conversation IDs are UUIDs of the social service, the same ones the REST API returns.
  The game and the player always come from the token, never from a frame.
- **Text only:** message bodies are plain text. The SDK must render them with `textContent`, never as HTML.

## 1. Connecting

Browsers cannot set headers on a WebSocket, so the player token goes in the query string:

```
GET /api/v1/ws?token=eyJ...
Origin: http://localhost:5174
```

The server checks, in this order, and answers with a plain HTTP error **before** the upgrade when a check fails:

| Check | HTTP answer | Error code |
|---|---|---|
| `token` missing, wrong signature or malformed | 401 | `missing_token` / `invalid_token` |
| token expired | 401 | `token_expired` |
| token is not a player token | 401 | `wrong_token_type` |
| `Origin` not in the allowed origins (see the note below) | 403 | `origin_not_allowed` |
| social cannot be reached (the server asks it for the player's own ID) | 503 | `social_unavailable` (retry with backoff) |
| the service is shutting down | 503 | `shutting_down` |
| game config cannot be loaded (platform down) | 503 | `not_ready` (not built yet) |

Requests without an `Origin` header (not from a browser, for example Postman) are not affected by the origin check.
**Today** the allowed origins are the service-wide `REALTIME_CORS_ORIGINS` list; the per-game `allowed_origins` list
needs Platform to store and serve it and is added later.

The body is the usual `{ "error": { "code": "...", "message": "..." } }`. After a successful upgrade the server
sends `hello` (below). Send nothing before you have received it.

Never log the full URL: the token is in it.

**Limits**

- A frame larger than **8 KB** closes the connection.
- A player may have **5** open connections (tabs). The sixth closes the **oldest** one with `1000`.
- The server pings the connection itself and closes it after 60 s without a pong; browsers answer automatically.
  This is separate from the `ping` frame below.

## 2. Frame envelope

```json
{ "type": "message.send", "id": "c-17", "data": { "conversationId": "...", "body": "hi" } }
```

| Field | Direction | Description |
|---|---|---|
| `type` | both | The frame type, from the tables below. |
| `data` | both | An object, `{}` when the frame has no data. |
| `id` | client → server | Optional string, up to 64 characters, chosen by the client. The server echoes it as `replyTo` in the `message.ack` or `error` that answers this frame. |

Server frames have no top-level `id`. Frames the server sends on its own (`message.new`, `presence.update`, ...)
are not replies and carry no `replyTo`.

## 3. Client → server frames

| Type | Data | Reply |
|---|---|---|
| `message.send` | `{ conversationId, body }` | `message.ack` or `error` |
| `conversation.read` | `{ conversationId, lastMessageId }` | none (`error` only if invalid) |
| `ping` | `{}` | `pong` |

### `message.send`

Send a text message to a conversation the player is a member of.

```json
{ "type": "message.send", "id": "c-17", "data": { "conversationId": "0193a6b4-...", "body": "gg" } }
```

- `body` is trimmed by the server. After trimming it must not be empty and must be at most the game's
  `max_message_length` characters (1 to 1000; characters, not bytes, so emoji and non-Latin text count fairly).
- If the game's profanity filter is on, banned words in the stored and broadcast body are replaced with `***`.
  The `message.new` frame carries the final text.
- The reply comes **after** the message is stored: `message.ack` on success, otherwise `error` and nothing is stored or
  sent to anyone. Possible error codes: `rate_limited`, `feature_disabled`, `not_member`, `blocked`, `empty`,
  `too_long`, `slow_mode`, `bad_frame`, `internal`.
- A resend after a reconnect can store the message twice; the SDK should not resend a message it has not seen
  acknowledged unless the user asks to.

### `conversation.read`

Tell the server the player has read the conversation up to a message, so `unread` goes down.
The server keeps the newest value; sending an older `lastMessageId` than before changes nothing.
No reply on success. The SDK sends it when the panel shows the conversation and when new messages arrive
while it is open.

### `ping`

Application-level liveness check for the SDK's own use. The server answers `pong` at once.

```json
{ "type": "ping", "id": "p-1", "data": {} }
```

## 4. Server → client frames

| Type | Data |
|---|---|
| `hello` | `{ playerId, configVersion, onlineFriends }` |
| `message.ack` | `{ replyTo, messageId, createdAt }` |
| `message.new` | `{ conversationId, message }` |
| `presence.update` | `{ playerId, status }` |
| `notification` | `{ kind, payload }` |
| `conversation.added` | `{ conversation }` |
| `conversation.removed` | `{ conversationId }` |
| `config.updated` | `{ version }` |
| `pong` | `{ replyTo }` |
| `error` | `{ code, message, replyTo, retryAfterMs }` |

`message` is the same object as in the REST history: `{ id, senderId, senderName, body, createdAt }`.
`conversation` is the same object as in the REST conversation list: `{ id, kind, clanId, members, lastMessage, unread }`.
See `Message` and `Conversation` in [`realtime.yaml`](realtime.yaml).

### `hello`

The first frame after the upgrade.

```json
{ "type": "hello", "data": { "playerId": "7f1e0c2a-...", "configVersion": 12, "onlineFriends": ["5a2b3c4d-..."] } }
```

- `playerId`: the caller's own ID, the social service's UUID for the player (not the game's own player ID from the token).
  The server learns it by asking social while the connection is being set up, which is why `social_unavailable` exists.
- `configVersion`: the version of the game's published config the server is using. If it differs from the
  version the SDK rendered with, fetch the public config again.
- `onlineFriends`: IDs of friends who are online right now. Empty when the game hides online status.

After `hello`, load `GET /api/v1/conversations` for the list.

### `message.ack`

The answer to a successful `message.send`.

```json
{ "type": "message.ack", "data": { "replyTo": "c-17", "messageId": "0193a6c0-...", "createdAt": "2026-11-09T18:20:00Z" } }
```

### `message.new`

A message was stored in a conversation the player is in. Sent to every open connection of every member,
including the sender's own other tabs. The tab that sent the message gets the `message.ack`; if it also receives
the `message.new`, it must not show the message twice (compare `message.id` with the ack's `messageId`).
Members who are offline get the message from history the next time they load it.

```json
{
  "type": "message.new",
  "data": {
    "conversationId": "0193a6b4-...",
    "message": { "id": "0193a6c0-...", "senderId": "5a2b3c4d-...", "senderName": "Rashad", "body": "gg", "createdAt": "2026-11-09T18:20:00Z" }
  }
}
```

### `presence.update`

A friend went online or offline. `status` is `online` or `offline`. A player counts as offline only after a
10 second grace period with no connection, so a page refresh does not flash them offline. Not sent at all when the game
turned online status off. When a new friendship starts and both players are online, each gets a `presence.update`
`online` for the other, right after `conversation.added`.

```json
{ "type": "presence.update", "data": { "playerId": "5a2b3c4d-...", "status": "online" } }
```

### `notification`

Something happened that the SDK should show as a notice. `kind` says what; `payload` depends on the kind.

| `kind` | Sent to | `payload` |
|---|---|---|
| `friend_request` | the player who received the request | `{ "requestId": "...", "from": { "id": "...", "name": "..." } }` |
| `friend_accepted` | both players | `{ "player": { "id": "...", "name": "..." } }` (the other player) |
| `clan_joined` | the clan's members | `{ "clanId": "...", "player": { "id": "...", "name": "..." } }` |
| `clan_kicked` | the player who was removed | `{ "clanId": "..." }` |
| `clan_disbanded` | the clan's members | `{ "clanId": "..." }` |

The SDK must ignore a `kind` it does not know, so new kinds can be added later without breaking old SDKs.
A notification can arrive twice (events are delivered at least once); showing it twice is acceptable, but a
`friend_request` should be matched by `requestId`.

### `conversation.added` / `conversation.removed`

The list of conversations changed: a friendship started, the player joined or created a clan, or the opposite.
`conversation.added` carries the full `conversation` object. The SDK ignores an `added` for an ID it already has.
In `members`, `online` is true while the player has an open WebSocket right now (the 10 second grace period and the game's
"hide online status" setting come with presence). `conversation.removed` carries only the `conversationId`; remove it from the list. Messages stay in the history
on the server, and a conversation returns with its history if the players become friends again.

```json
{ "type": "conversation.removed", "data": { "conversationId": "0193a6b4-..." } }
```

### `config.updated`

The game published a new config. Sent to every open connection of the game. Fetch the public config again
(after a random 0 to 500 ms wait, so all players do not hit the server in the same second) and redraw. `version`
is the new version.

```json
{ "type": "config.updated", "data": { "version": 13 } }
```

### `pong`

The answer to `ping`: `{ "type": "pong", "data": { "replyTo": "p-1" } }`.

### `error`

A client frame was rejected. The connection stays open unless a close code in section 6 says otherwise.

```json
{ "type": "error", "data": { "code": "slow_mode", "message": "Wait a moment before sending again", "replyTo": "c-17", "retryAfterMs": 1400 } }
```

| Field | Description |
|---|---|
| `code` | Stable machine-readable code, section 5. |
| `message` | Human-readable sentence. Do not parse it. |
| `replyTo` | The `id` of the client frame that caused it. `null` when that frame had no `id` or could not be read. |
| `retryAfterMs` | Only for `slow_mode`: how long to wait before the next message, in milliseconds. Absent otherwise. |

## 5. Error codes

| Code | When | What the SDK does |
|---|---|---|
| `rate_limited` | More than 5 `message.send` frames in 5 seconds. Three in a row also close the connection with `1008`. | Slow down; keep the text in the input. |
| `feature_disabled` | The game turned off direct messages or clan chat for this kind of conversation. | Hide or disable the input. |
| `not_member` | The player is not in the conversation (also for an unknown ID or one of another game). | Reload the conversation list. |
| `blocked` | One of the two players blocked the other (direct messages only). | Show "you can't message this player". |
| `empty` | The body is empty after trimming. | Do not send empty messages. |
| `too_long` | The body is longer than the game's `max_message_length`. | Show the limit; the SDK checks first too. |
| `slow_mode` | The player sent a message sooner than the game's slow mode allows. Has `retryAfterMs`. | Disable sending until the time has passed. |
| `bad_frame` | Not valid JSON, not an object, unknown `type`, a missing or wrong-type field, or a binary frame. | A bug in the SDK; log it. |
| `internal` | The server failed, for example the database is down. Nothing was stored. | Let the user retry later. |

Errors are for client frames only. Problems before the upgrade are plain HTTP errors (section 1).

## 6. Close codes

| Code | Meaning | SDK action |
|---|---|---|
| `1000` | Normal close (also: the oldest connection when a sixth opens) | None |
| `1001` | Server shutting down | Reconnect with backoff |
| `1008` | Policy: rate limit hit too often | Reconnect after 30 s |
| `1009` | A frame was larger than 8 KB | A bug in the SDK; do not resend the frame. Reconnect with backoff |
| `4001` | Token expired | Get a new token from the game (`getToken()`) and reconnect now |
| `4002` | Slow client: the server could not send fast enough (64 frames were waiting) | Reconnect with backoff |
| `4003` | The game's config turned chat off | Do not reconnect. Fetch the public config again later (for example on the next page load) and connect only if chat is on |

The close reason text is for humans only. Backoff starts at 1 s, doubles to at most 30 s, and adds random jitter; reset it
after a connection stays open for 10 s.

After any reconnect the server forgets what the client has seen: send nothing, wait for `hello`, then reload the conversation
list and the history of the open conversation (frames sent while disconnected are not replayed).

## 7. Example session

```
client → GET /api/v1/ws?token=...           (HTTP upgrade)
server → {"type":"hello","data":{"playerId":"7f1e...","configVersion":12,"onlineFriends":["5a2b..."]}}
client → GET /api/v1/conversations           (REST)
client → {"type":"message.send","id":"c-1","data":{"conversationId":"0193...","body":"hello"}}
server → {"type":"message.ack","data":{"replyTo":"c-1","messageId":"0193a6c0...","createdAt":"2026-11-09T18:20:00Z"}}
server → {"type":"message.new","data":{"conversationId":"0193...","message":{"id":"0193a6c0...","senderId":"7f1e...","senderName":"Ali","body":"hello","createdAt":"2026-11-09T18:20:00Z"}}}
client → {"type":"message.send","id":"c-2","data":{"conversationId":"0193...","body":"again"}}
server → {"type":"error","data":{"code":"slow_mode","message":"Wait a moment before sending again","replyTo":"c-2","retryAfterMs":1400}}
server → {"type":"config.updated","data":{"version":13}}
server ✕ close 4001                           (token expired: get a new token, reconnect)
```
