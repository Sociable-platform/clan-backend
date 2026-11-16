# Realtime WebSocket protocol

The live half of the realtime service: new messages, presence, notifications and live re-skin all arrive on one
WebSocket. The two REST reads (conversation list and history) are in [`realtime.yaml`](realtime.yaml).

- **Status:** contract for the SDK. Every frame the server sends has the envelope of a queue event (section 2). Built so far: the endpoint with the token and origin checks, the `hello` frame, `ping`/`pong`,
  the 8 KB limit, `bad_frame` errors, the server's own keepalive ping (every 25 s), the close codes `1001`, `1008`, `1009`, `4001`, `4002` and `4403`,
  the friend frames (`conversation.added`, `conversation.updated`, the `friend_request`, `friend_request_cancelled`, `friend_request_declined` and `friend_accepted` notifications), and
  `message.send` with `message.ack` and `message.new`, and `conversation.read`.
  `message.send` follows the game's message rules (see `message.send` below): a rate limit per connection, the game's chat switches, blocks,
  its maximum length, slow mode and the profanity filter. Answers: `message.ack`, or `error` with `rate_limited`, `not_member`,
  `feature_disabled`, `blocked`, `empty`, `too_long`, `slow_mode`, `bad_frame` or `internal`.
  In `hello`, `configVersion` is the version of the game's published config (`0` when the game has none or Platform did not answer within a second)
  and `onlineFriends` lists the friends who are online. `config.updated` is sent when the game publishes a new config.
  `presence.update` tells a friend came online, went offline (after a 10 second grace period) or, for a new friendship, that the other one is online.
  Clan rooms: `conversation.added` and `conversation.removed` for a clan's chat room, and the `clan_joined`, `clan_kicked`
  and `clan_disbanded` notifications, made from Social's clan events.
  Not built yet: the 5-connections-per-player limit, and `4003`.
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

**A suspended game.** When an admin has switched the game (or its developer) off, the upgrade still succeeds, because a
close code can only be sent on a WebSocket, but the server closes the connection at once with code `4403` and reason
`game_suspended`, and sends **no** `hello`. The same close ends every connection of the game that is open when the game is
suspended, within seconds. See section 6.

Never log the full URL: the token is in it.

**Limits**

- A frame larger than **8 KB** closes the connection.
- A player may have **5** open connections (tabs). The sixth closes the **oldest** one with `1000`.
- The server pings the connection itself and closes it after 60 s without a pong; browsers answer automatically.
  This is separate from the `ping` frame below.

## 2. Frame envelope

**Every frame the server sends has the same envelope as an event on the queue** (`events.Event` in `pkg/events`), so the SDK
reads one message format:

```json
{
  "event_id": "3f1c0b7e-9a2d-4c51-8e0f-6b7a1d2c3e4f",
  "type": "message.new",
  "occurred_at": "2026-11-09T18:20:00.314Z",
  "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15",
  "data": { "conversationId": "0193a6b4-...", "message": { "...": "..." } }
}
```

| Field | Direction | Description |
|---|---|---|
| `event_id` | server → client | A random UUID for this one frame. The same frame sent to several players (for example `message.new` to every member) has one `event_id`. It is new for every frame: a queue event that is delivered twice makes two frames with two IDs, so it cannot be used to spot that case; match by the content (conversation ID, message ID) as before. |
| `type` | both | The frame type, from the tables below. |
| `occurred_at` | server → client | When it happened, RFC 3339 in UTC to the millisecond: when the message was stored (`message.ack`, `message.new`), the time of the queue event for frames that come from one (`conversation.added`, notifications, ...), the time of sending for `hello`, `pong` and `error`. |
| `game_id` | server → client | The game of the connection. It is always the game of the player's token. |
| `data` | both | An object, `{}` when the frame has no data. |
| `id` | client → server | Optional string, up to 64 characters, chosen by the client. The server echoes it as `replyTo` in the `message.ack` or `error` that answers this frame. |

**Frames the client sends have no envelope.** They are `{ "type": "...", "id": "...", "data": { ... } }`, because the game always comes
from the verified token, never from a frame (a `game_id` in a client frame is ignored).

The envelope fields come before `data`; `type` and `data` are where they always were, so a client that only reads those two keeps working.
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
  sent to anyone. Every `error` has `replyTo` (the frame's `id`). Possible error codes: `rate_limited`, `feature_disabled`, `not_member`, `not_friends`, `blocked`, `empty`,
  `too_long`, `slow_mode`, `bad_frame`, `internal`.
- **The checks run in this order, and the first one that fails answers:**

  | # | Check | Error |
  |---|---|---|
  | 1 | Rate limit of the connection: at most 5 `message.send` frames in any 5 seconds (every `message.send` counts, even a malformed one; refused ones do not). A refused frame is not looked at further. Three refused frames in a row (no accepted one between them) also close the connection with `1008`, after their error frames. | `rate_limited` |
  | 2 | The body is not empty after trimming and has no NUL character | `empty`, `bad_frame` |
  | 3 | The player is a member of the conversation | `not_member` |
  | 4 | The game turned this chat on: `chat.enabled`, and `chat.directMessages` for a DM or `chat.clanChat` for a clan room | `feature_disabled` |
  | 5 | A DM is not read-only (`readOnly`: the two are not friends any more; nobody can send until they are friends again) | `not_friends` |
  | 6 | In a DM, neither player blocked the other (a block works both ways, and clan rooms are not affected) | `blocked` |
  | 7 | The body is at most `chat.maxMessageLength` characters | `too_long` |
  | 8 | Slow mode: `chat.slowModeSeconds` since this player's last stored message, counted across all their tabs and conversations. `retryAfterMs` says how long to wait. | `slow_mode` |
  | 9 | The profanity filter, if `chat.profanityFilter` is on: whole words, compared in lower case, replaced with `***` | (no error) |

  Refused messages do not start the slow mode, and neither does one that could not be stored. The rules are those of the game's **published** config,
  which Realtime keeps in memory and updates on `config.published`. A game with no published config, or one Realtime cannot read yet (Platform down
  and nothing cached), has chat on, up to 1000 characters, no slow mode and no filter.
- `message.ack` is sent to the tab that sent the message; `message.new` goes to **every** open tab of every member, the sending tab included (it
  comes right after the ack, with the same `messageId` and `createdAt`, so the SDK can show the message once).
- A resend after a reconnect can store the message twice; the SDK should not resend a message it has not seen
  acknowledged unless the user asks to.

### `conversation.read`

Tell the server the player has read the conversation up to a message, so `unread` goes down.
The server keeps the newest value; sending an older `lastMessageId` than before changes nothing.
No reply on success. The SDK sends it when the panel shows the conversation and when new messages arrive
while it is open.

`lastMessageId` must be a message of that conversation. Otherwise the answer is an `error` frame and nothing changes:
`not_member` (the player is not in the conversation, also for an unknown ID or one of another game) or `bad_frame` (a
missing or wrong-type field, an ID that is not a UUID, or a message that is not in that conversation). The new position
shows in `unread` of `GET /api/v1/conversations`; other open tabs of the player are not told.

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
| `conversation.updated` | `{ conversation }` |
| `config.updated` | `{ version }` |
| `pong` | `{ replyTo }` |
| `error` | `{ code, message, replyTo, retryAfterMs }` |

`message` is the same object as in the REST history: `{ id, kind, senderId, senderName, body, createdAt }`.
`kind` is `text` for a message a player wrote, or a member line in a clan room (`member_joined`, `member_left`,
`member_kicked`, see "Member lines" below).
`conversation` is the same object as in the REST conversation list: `{ id, kind, clanId, members, lastMessage, unread }`.
See `Message` and `Conversation` in [`realtime.yaml`](realtime.yaml).

### `hello`

The first frame after the upgrade.

```json
{
  "event_id": "6bfe80b7-7e37-4eba-80bf-85c0bcace6ee",
  "type": "hello",
  "occurred_at": "2026-11-09T18:19:00.120Z",
  "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15",
  "data": { "playerId": "7f1e0c2a-...", "configVersion": 12, "onlineFriends": ["5a2b3c4d-..."] }
}
```

- `playerId`: the caller's own ID, the social service's UUID for the player (not the game's own player ID from the token).
  The server learns it by asking social while the connection is being set up, which is why `social_unavailable` exists.
- `configVersion`: the version of the game's published config the server is using. If it differs from the
  version the SDK rendered with, fetch the public config again.
- `onlineFriends`: IDs of the friends who are online right now, a friend being someone the player has a DM with. A friend who closed their
  last tab less than 10 seconds ago still counts as online (the grace period). Empty when there are none, or when the friends cannot be looked up.

After `hello`, load `GET /api/v1/conversations` for the list.

### `message.ack`

The answer to a successful `message.send`.

```json
{
  "event_id": "24ea28ac-2875-4bb7-a1d2-2b24393065ef",
  "type": "message.ack",
  "occurred_at": "2026-11-09T18:20:00.314Z",
  "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15",
  "data": { "replyTo": "c-17", "messageId": "0193a6c0-...", "createdAt": "2026-11-09T18:20:00.314Z" }
}
```

### `message.new`

A message was stored in a conversation the player is in. Sent to every open connection of every member,
including the sender's own other tabs. The tab that sent the message gets the `message.ack`; if it also receives
the `message.new`, it must not show the message twice (compare `message.id` with the ack's `messageId`).
Members who are offline get the message from history the next time they load it.

```json
{
  "event_id": "4a30586e-8521-44df-9817-b2204fa560fc",
  "type": "message.new",
  "occurred_at": "2026-11-09T18:20:00.314Z",
  "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15",
  "data": {
    "conversationId": "0193a6b4-...",
    "message": { "id": "0193a6c0-...", "kind": "text", "senderId": "5a2b3c4d-...", "senderName": "Rashad", "body": "gg", "createdAt": "2026-11-09T18:20:00.314Z" }
  }
}
```

### `presence.update`

A friend came online, went offline, or a new friend is online. `status` is `online` or `offline`.

- **Offline:** a player counts as offline only after a 10 second grace period with no open connection, so a page refresh does not flash them
  offline. It is sent to the player's friends who are connected. A player who connects again within the 10 seconds was never offline: nothing is sent.
- **Online, coming online** (TEAM-274): when a player's first connection opens and they were offline, their friends who are connected get
  `online` for them, right after the player's own `hello`. Only the first connection counts: another tab of a player who is already connected
  sends nothing, and neither does coming back within the 10 second grace period (a page refresh), since the player never went offline.
- **Online, new friend:** when a new friendship starts and both players are online, each gets `online` for the other, right after
  `conversation.added`.
- Treat a friend as online from `hello.onlineFriends` or from an `online` frame until an `offline` arrives.
- Frames can be lost while the SDK is disconnected, so after every reconnect take the state from the new `hello`, not from the old frames.

```json
{
  "event_id": "6847effe-a146-43cf-a37e-eff6f6fd7e9f",
  "type": "presence.update",
  "occurred_at": "2026-11-09T18:21:05.000Z",
  "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15",
  "data": { "playerId": "5a2b3c4d-...", "status": "online" }
}
```

### `notification`

Something happened that the SDK should show as a notice. `kind` says what; `payload` depends on the kind.

| `kind` | Sent to | `payload` |
|---|---|---|
| `friend_request` | the player who received the request | `{ "requestId": "...", "from": { "id": "...", "name": "..." } }` (`requestId` is `null` until Social puts the request ID in its event) |
| `friend_request_cancelled` | the player who received the request, on every open tab | `{ "requestId": "...", "from": { "id": "...", "name": "..." } }`: the sender took the request back. Remove the `friend_request` with this `requestId`. |
| `friend_request_declined` | the player who received the request, on every open tab | `{ "requestId": "...", "from": { "id": "...", "name": "..." } }`: this player declined (maybe in another tab). Remove the `friend_request` with this `requestId`. The sender is **not** told. |
| `friend_accepted` | both players | `{ "player": { "id": "...", "name": "..." }, "requestId": "..." }` (`player` is the other player; `requestId` is the request that was accepted, `null` until Social puts it in its event; remove the matching `friend_request`) |
| `clan_joined` | the other members of the clan (the player who joined gets `conversation.added` instead) | `{ "clanId": "...", "player": { "id": "...", "name": "..." } }` |
| `clan_kicked` | the player who was removed | `{ "clanId": "..." }` |
| `clan_disbanded` | everybody who was still in the clan, the last member included | `{ "clanId": "..." }` |

The SDK must ignore a `kind` it does not know, so new kinds can be added later without breaking old SDKs.
A notification can arrive twice (events are delivered at least once); showing it twice is acceptable, but a
`friend_request` should be matched by `requestId`.

### `conversation.added` / `conversation.removed`

The list of conversations changed: a friendship started, the player joined or created a clan, or left it. An ended
friendship does not remove the DM: it becomes read-only (`conversation.updated`, below).
`conversation.added` carries the full `conversation` object. An `added` for an ID the SDK already has replaces that conversation (it opens a read-only DM again).
In `members`, `online` is true while the player has an open WebSocket or closed the last one less than 10 seconds ago. `conversation.removed` carries only the `conversationId`; remove it from the list.

### `conversation.updated`: a DM becomes read-only when the friendship ends

When two players stop being friends, because one removed or blocked the other, their DM does not disappear: both
keep it with its history, read-only (TEAM-283). Each of them gets `conversation.updated` with the full
`conversation`, now with `readOnly: true` and both members `online: false`; replace the conversation with the same
`id` (keep its loaded messages). Nobody is told who ended it. In a read-only DM nobody can send (`not_friends`), and
it is not a friendship: no `presence.update` goes either way, and `GET /api/v1/conversations` lists it with
`readOnly: true` and both members offline. It stays read-only after an unblock (unblocking does not make them
friends). When they become friends again the same conversation comes back as `conversation.added` without
`readOnly` (an SDK that already has it must update it, not ignore it), with the messages and each player's read
position kept. `readOnly` is sent only when it is true.

A clan room (`kind` `clan`, with the clan's `clanId`) is added when the player creates or joins the clan, and removed
when they leave or are kicked. It comes with its whole history: the messages written before the player joined, and
while they were away if they were a member before; `unread` counts the other members' messages from the start of the
history. The members who stay get no `conversation.*` frame when someone leaves (their member list is right on the next
`GET /api/v1/conversations`), but they get the member line below. When the clan is disbanded the room is deleted with its messages and everybody still in
it gets `conversation.removed`. In a clan room `online` is the state when the room was sent: live `presence.update`
frames go to friends only.

```json
{
  "event_id": "18d90869-5856-48b9-844a-ab2d3b5ab5f9",
  "type": "conversation.removed",
  "occurred_at": "2026-11-09T18:22:10.000Z",
  "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15",
  "data": { "conversationId": "0193a6b4-..." }
}
```

#### Member lines: joins, leaves and kicks in a clan room

When a player joins a clan, leaves it or is kicked, Realtime stores a member line in the clan room and sends it as
`message.new`, so it is in the chat live and in the history later (TEAM-291). Its `kind` is `member_joined`,
`member_left` or `member_kicked`; `senderId` / `senderName` are the player who joined or left (the name they had in
the room) and `body` is empty: the widget writes the sentence ("Bob joined the clan"), so a game can change or
translate it.

- `member_joined` goes to every member, the joiner included; the joiner's `conversation.added` already has it as
  `lastMessage`. Only a new membership writes one: a repeated `clan.member_joined` writes no second line.
- `member_left` (`member_kicked` when the reason is `kicked`) goes to the members who stay. The player who left gets
  `conversation.removed` as before, not the line.
- The clan owner's room (`clan.created`) starts without a line.
- Member lines are never unread: `unread` counts `text` messages only, and a member line gives nobody a badge.
- Players cannot send them: `message.send` always stores `text`.

An SDK that does not know a `kind` should skip the message rather than show it as an empty bubble.

```json
{
  "event_id": "5b7e0f6c-2a4d-4e1b-9c3f-8d6a1e2b4c70",
  "type": "message.new",
  "occurred_at": "2026-11-09T18:22:10.000Z",
  "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15",
  "data": {
    "conversationId": "0193a6b4-...",
    "message": { "id": "0193a6bd-...", "kind": "member_left", "senderId": "5a2b3c4d-...", "senderName": "Rashad", "body": "", "createdAt": "2026-11-09T18:22:10.006Z" }
  }
}
```

### `config.updated`

The game published a new config. Sent to every open connection of the game. Fetch the public config again
(after a random 0 to 500 ms wait, so all players do not hit the server in the same second) and redraw. `version`
is the new version, the one the server's config cache holds. If Platform cannot be reached when the event arrives, the server tries again
after a second, and if that fails too, the frame is not sent: that is why `hello` carries `configVersion` (compare it after a reconnect).

```json
{
  "event_id": "dd8baa14-ed04-4ddb-9f45-d19bde912e9a",
  "type": "config.updated",
  "occurred_at": "2026-11-09T18:23:00.000Z",
  "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15",
  "data": { "version": 13 }
}
```

### `pong`

The answer to `ping`, with the envelope of every server frame: `{ "event_id": "...", "type": "pong", "occurred_at": "...", "game_id": "...", "data": { "replyTo": "p-1" } }`.

### `error`

A client frame was rejected. The connection stays open unless a close code in section 6 says otherwise.

```json
{
  "event_id": "9f98fcb4-27e8-4ad0-83d5-8ac7a51b4f77",
  "type": "error",
  "occurred_at": "2026-11-09T18:20:03.000Z",
  "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15",
  "data": { "code": "slow_mode", "message": "Wait a moment before sending again", "replyTo": "c-17", "retryAfterMs": 1400 }
}
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
| `rate_limited` | More than 5 `message.send` frames in 5 seconds on this connection. Three refused in a row also close the connection with `1008`. | Slow down; keep the text in the input. |
| `feature_disabled` | The game turned off direct messages or clan chat for this kind of conversation. | Hide or disable the input. |
| `not_member` | The player is not in the conversation (also for an unknown ID or one of another game). | Reload the conversation list. |
| `not_friends` | The DM is read-only: the two are not friends any more (`readOnly` on the conversation). | Keep the conversation readable; disable the input. |
| `blocked` | One of the two players blocked the other (direct messages only). The player is not told who blocked whom. | Show "you can't message this player". |
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
| `4403` | The game is suspended (reason `game_suspended`): an admin switched off the game or its developer. Sent instead of `hello` to a new connection, and to every open connection when the suspension happens. It is not a network drop | Do not reconnect in a loop: remove the panel and stop. Try again on the next page load. If the game was resumed, that connects normally |

The close reason text is for humans only. Backoff starts at 1 s, doubles to at most 30 s, and adds random jitter; reset it
after a connection stays open for 10 s.

After any reconnect the server forgets what the client has seen: send nothing, wait for `hello`, then reload the conversation
list and the history of the open conversation (frames sent while disconnected are not replayed).

## 7. Example session

```
client → GET /api/v1/ws?token=...           (HTTP upgrade)
server → {"event_id":"6bfe...","type":"hello","occurred_at":"2026-11-09T18:19:00.120Z","game_id":"8d4e...","data":{"playerId":"7f1e...","configVersion":12,"onlineFriends":["5a2b..."]}}
client → GET /api/v1/conversations           (REST)
client → {"type":"message.send","id":"c-1","data":{"conversationId":"0193...","body":"hello"}}
server → {"event_id":"24ea...","type":"message.ack","occurred_at":"2026-11-09T18:20:00.314Z","game_id":"8d4e...","data":{"replyTo":"c-1","messageId":"0193a6c0...","createdAt":"2026-11-09T18:20:00.314Z"}}
server → {"event_id":"4a30...","type":"message.new","occurred_at":"2026-11-09T18:20:00.314Z","game_id":"8d4e...","data":{"conversationId":"0193...","message":{"id":"0193a6c0...","kind":"text","senderId":"7f1e...","senderName":"Ali","body":"hello","createdAt":"2026-11-09T18:20:00.314Z"}}}
client → {"type":"message.send","id":"c-2","data":{"conversationId":"0193...","body":"again"}}
server → {"event_id":"9f98...","type":"error","occurred_at":"2026-11-09T18:20:02.000Z","game_id":"8d4e...","data":{"code":"slow_mode","message":"Wait a moment before sending again","replyTo":"c-2","retryAfterMs":1400}}
server → {"event_id":"dd8b...","type":"config.updated","occurred_at":"2026-11-09T18:23:00.000Z","game_id":"8d4e...","data":{"version":13}}
server ✕ close 4001                           (token expired: get a new token, reconnect)
```
