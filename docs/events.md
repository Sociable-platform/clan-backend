# Events: how services tell each other what happened

Services never call each other to say "something changed". The service that changes data **publishes an event** to
RabbitMQ after the change is saved, and every service that cares **consumes** it from its own queue. Today Social
publishes and Realtime consumes. This file is the single place that says how that works and which field names both sides
must agree on.

If you change an event, change this file, the producer, the consumer and their tests in the same merge request.

## 1. The flow in one picture

```
 game player (browser)
        │  POST /api/v1/friends/requests/{id}/accept        (Social REST, player token)
        ▼
 ┌─────────────── social ───────────────┐
 │ handler → service → repository       │
 │   1. save the friendship (Postgres)  │
 │   2. COMMIT                          │
 │   3. events.Publish(friend.accepted) │   ← only after the commit; a failed publish never fails the request
 └──────────────────┬───────────────────┘
                    │ AMQP, persistent message, routing key = event type, publisher confirm
                    ▼
        RabbitMQ  exchange  socialkit.events  (topic, durable)
                    │  bindings: config.published, friend.*, player.*, clan.*
                    ▼
        queue  realtime.events  (durable)
                    │  one consumer, prefetch 20, manual ack
                    ▼
 ┌─────────────── realtime ─────────────────────────────────────┐
 │ consumer.Dispatcher  decode envelope → decode data → call     │
 │ service.FriendAccepted  validate → repository.UpsertDM (DB)   │
 │ returns the frames to send → hub.SendToPlayers                │
 └──────────────────┬───────────────────────────────────────────┘
                    │ WebSocket frames
                    ▼
 both players' open tabs:  conversation.added + notification(friend_accepted)
```

What each hop guarantees:

| Hop | Guarantee |
|---|---|
| Social saves, then publishes | The event never announces something that was rolled back. |
| Publisher → RabbitMQ | The publisher waits for the broker's confirm (up to 5 s) and the message is persistent. |
| RabbitMQ → queue | The queue is durable and exists before Realtime does, so events wait for a Realtime that is down. |
| Queue → consumer | At least once. The consumer acknowledges only after the database write. |
| Consumer → players | Best effort: a player who is not connected simply misses the frame and loads the state over REST later. |

## 2. Where the code lives

| Piece | Path |
|---|---|
| Envelope, `Publisher` interface, `Noop`, `Safe`, `Recorder` (test fake) | `pkg/events/events.go` |
| RabbitMQ publisher (confirms, lazy connect, reconnect) | `pkg/events/rabbitmq.go` |
| Where Social publishes friend events | `services/social/internal/service/friends.go` |
| Social wiring (`SOCIAL_RABBITMQ_URL`) | `services/social/cmd/main.go` |
| Exchange, queue and bindings | `deploy/rabbitmq/definitions.json`, applied by the `rabbitmq-setup` job in `docker-compose.yml` |
| Consumer loop (connect, ack, retry, reconnect) | `services/realtime/internal/consumer/consumer.go` |
| Decoding and routing by event type | `services/realtime/internal/consumer/dispatch.go` |
| Rules and DB work per event | `services/realtime/internal/service/events.go`, `services/realtime/internal/repository/conversations.go` |
| Realtime wiring (`REALTIME_RABBITMQ_URL`, shared hub) | `services/realtime/cmd/main.go` |
| What the player's browser receives | `openapi/realtime-ws.md` |
| Test that pins Social's field names | `TestDispatchReadsEventsAsSocialPublishesThem` in `services/realtime/internal/consumer/dispatch_test.go` |

Realtime decodes the same `events.Event` struct that Social encodes, so a renamed envelope field breaks both sides at
compile time. The `data` fields are plain maps and are **not** checked by the compiler: that is what the test above is for.

## 3. The broker: exchange, queue, bindings

| Name | Kind | Notes |
|---|---|---|
| `socialkit.events` | exchange, type `topic`, durable | Every service publishes here. The routing key is the event `type`. |
| `realtime.events` | queue, durable | Realtime's own queue. |

Bindings from `socialkit.events` to `realtime.events`: `config.published`, `friend.*`, `player.*`, `clan.*`.
So Realtime also receives events it does not handle yet (see section 6); it acknowledges and ignores them.

`docker compose up` runs the one-shot `rabbitmq-setup` job, which imports `deploy/rabbitmq/definitions.json` through the
management API. It only adds what is missing and never deletes messages. A new queue or binding is added to that file in
the same merge request as the code that needs it. A routing key that no queue is bound to is dropped by the broker.

Management dashboard: http://localhost:15672 (log in with `RABBITMQ_USER` / `RABBITMQ_PASSWORD` from `.env`).

## 4. The envelope (every event)

A message body is JSON. AMQP properties set by the publisher: `delivery_mode` 2 (persistent), `content_type`
`application/json`, `message_id` = `event_id`, `type` = event type, `timestamp` = `occurred_at`.

| Field | Type | Meaning |
|---|---|---|
| `event_id` | string (UUID v4) | Unique per event. Can be used to notice the same event twice. Realtime logs it. |
| `type` | string | The event name and the routing key, e.g. `friend.accepted`. |
| `occurred_at` | string, RFC 3339 UTC | When the event was made, e.g. `2026-11-10T08:00:00Z`. Anything else makes Realtime drop the event. |
| `game_id` | string (UUID) | The game the event belongs to. Realtime takes the tenant from here and every query filters by it. |
| `data` | object | Fields that depend on `type` (section 5). Left out when empty. |

```json
{
  "event_id": "3f1c0b7e-9a2d-4c51-8e0f-6b7a1d2c3e4f",
  "type": "friend.accepted",
  "occurred_at": "2026-11-10T08:00:00Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": { "players": [ { "id": "…", "name": "Ali" }, { "id": "…", "name": "Rashad" } ] }
}
```

**Players in `data`** are always `{ "id", "name" }`: `id` is **Social's own player ID** (a UUID, not the game's player ID
like `u_42`), `name` the display name at the time of the event. Realtime stores both in `conversation_members`.

## 5. Event catalogue

### `friend.request_sent`

- **Published by Social** after `POST /api/v1/friends/requests` created a new pending request (HTTP 201).
- **`data`:**

| Field | Type | Required by Realtime | Meaning |
|---|---|---|---|
| `sender` | `{ id, name }` | yes | The player who sent the request. |
| `receiver` | `{ id, name }` | yes (`id`) | The player who got it. |
| `request_id` | string | **no, and Social does not send it yet** | The request's ID. |

```json
{ "sender": { "id": "c1a27abb-…", "name": "Ali" }, "receiver": { "id": "7d2f1c0e-…", "name": "Rashad" } }
```

- **Realtime does:** nothing is stored. It sends one `notification` frame to the **receiver** only.
- **Without `request_id`** the frame has `requestId: null`, so the SDK cannot tell from the notification which request to
  accept (it can load `GET /api/v1/friends/requests`). Social should add `"request_id": req.ID`; Realtime already uses it.
- A duplicate delivery shows the notification twice (acceptable; the SDK matches by `requestId` once it exists).

### `friend.accepted`

- **Published by Social** when two players become friends, by either path:
  `POST /api/v1/friends/requests/{requestID}/accept` (200), or `POST /api/v1/friends/requests` when the other player had
  already asked (200, accepted at once).
- **`data`:**

| Field | Type | Meaning |
|---|---|---|
| `players` | array of exactly 2 × `{ id, name }` | The two new friends, the smaller ID first. Any other length: the event is dropped. |

```json
{ "players": [ { "id": "2c33a183-…", "name": "Dio" }, { "id": "9f4bb1f8-…", "name": "Jojo" } ] }
```

- **Realtime does**, in one database transaction:
  1. finds or creates the DM conversation of the pair in that game, keyed by `dm_key` = `<smaller player UUID>:<larger player UUID>`
     (lower case), so the order in the event does not matter;
  2. inserts both players as members (or updates their names);
  3. reads the last message and each member's unread count.
  Then it sends to **each** player: `conversation.added` (their own `unread`), and a `notification` `friend_accepted`
  naming the **other** player.
- If the players were friends before and were removed, the **same conversation comes back with its history**.
- Running it twice changes nothing in the database; the frames are sent again, and the SDK ignores a conversation id it knows.

### `friend.removed`  (not published yet)

- **Not published by Social yet.** Realtime already handles it, so the field names below are what Social must publish
  (or both sides change them together, e.g. to `players` like `friend.accepted`).
- **`data`:** `{ "a_id": "<uuid>", "b_id": "<uuid>" }`: the two former friends, Social player IDs, either order.
- **Realtime does:** deletes both rows in `conversation_members` of their DM. The conversation and its **messages stay**.
  Sends `conversation.removed` with the conversation id to both players. If the game has no DM for the pair (the accept was
  never seen), it does nothing.

## 6. Events that arrive but are not handled yet

The queue is bound to `config.published`, `player.*` and `clan.*`. Realtime **acknowledges and ignores** any type it has no
handler for (it logs them at debug level), so they never block the queue. Each is handled by its own task:

| Event | Task |
|---|---|
| `clan.created`, `clan.member_joined`, `clan.member_left`, `clan.disbanded` | clan chat rooms (TEAM-147) |
| `player.blocked`, `player.unblocked` | blocks sync (TEAM-151, TEAM-154) |
| `config.published` | live re-skin (TEAM-148) |

## 7. What the player's browser receives

Frames are `{ "type": "...", "data": { ... } }` over the WebSocket. Full contract: `openapi/realtime-ws.md`.

**`notification` / `friend_request`** (to the receiver):
```json
{ "type": "notification", "data": { "kind": "friend_request",
  "payload": { "requestId": null, "from": { "id": "c1a27abb-…", "name": "Ali" } } } }
```

**`notification` / `friend_accepted`** (to each player; `player` is the other one):
```json
{ "type": "notification", "data": { "kind": "friend_accepted",
  "payload": { "player": { "id": "7d2f1c0e-…", "name": "Rashad" } } } }
```

**`conversation.added`** (to each player; `unread` is theirs, `online` = has an open WebSocket right now):
```json
{ "type": "conversation.added", "data": { "conversation": {
  "id": "128cd7f6-…", "kind": "dm", "clanId": null,
  "members": [ { "id": "9f4bb1f8-…", "name": "Jojo", "online": true }, { "id": "2c33a183-…", "name": "Dio", "online": true } ],
  "lastMessage": null, "unread": 0 } } }
```

**`conversation.removed`** (to both players):
```json
{ "type": "conversation.removed", "data": { "conversationId": "128cd7f6-…" } }
```

Order per player after `friend.accepted`: `conversation.added`, then `notification`. Chat messages are **not** events: they
travel over the WebSocket only (`message.send` / `message.new`, see `openapi/realtime-ws.md`).

## 8. The publisher (Social side)

- Built in `services/social/cmd/main.go` from `SOCIAL_RABBITMQ_URL`. Without it Social uses `events.Noop{}`, logs
  `SOCIAL_RABBITMQ_URL is not set`, and drops its events. docker compose sets it.
- Social never stops or fails because of the broker: the publisher **connects on the first event**, not at startup, and
  `events.Safe` turns a failed publish into a log line (`publish event failed`, with `event_id` and `type`) instead of an error.
- After a failed connection the publisher refuses new attempts for 5 s, so requests do not queue up behind a dead broker.
  Each publish waits at most 5 s for the broker's confirm.
- Publishing happens **after the database commit**. Tests use `events.Recorder` to check which events were published.
- To publish a new event: `s.events.Publish(ctx, events.New("friend.removed", gameID, map[string]any{...}))`.

## 9. The consumer (Realtime side)

- Started in `services/realtime/cmd/main.go` if `REALTIME_RABBITMQ_URL` is set **and** a database is configured (it would
  have nowhere to store events otherwise). Not set: the log says `REALTIME_RABBITMQ_URL is not set` and Realtime runs without events.
- **One consumer goroutine**, so events are handled in arrival order (a `friend.removed` never overtakes the
  `friend.accepted` before it). Prefetch 20, manual acknowledgement **after** the database write.
- Each handler call has a 10 s limit, and a panic counts as a failure instead of crashing the service.

What happens to a message:

| Outcome of handling | What the consumer does | Log line |
|---|---|---|
| Success | `ack` | `event handled` (event_id, type, game_id, number of frames) |
| Type not handled yet | `ack`, ignore | (debug) `ignoring event nobody handles yet` |
| **Unusable** event: not JSON, bad `occurred_at`, missing or wrong-shaped `data`, an ID that is not a UUID, a blank name, the same player twice, `players` not exactly 2 | rejected **without requeue** at once | `dropping unusable event` + the reason |
| **Transient** failure (e.g. database down), first time | waits 1 s, then `nack` with requeue | `event failed, will try once more` |
| Transient failure, second time (message is marked redelivered) | rejected without requeue | `event failed twice, dropping it` |

There is **no dead-letter queue yet**, so a dropped event is gone (see section 11).

- **Reconnect:** if the broker is down or restarts, Realtime keeps serving WebSockets and reconnects with a growing wait
  (1 s, 2 s, 4 s … up to 30 s; reset after a successful connection). Log: `event consumer stopped, will reconnect`, then
  `event consumer started`. The broker password is never logged (only the host).
- **The queue must already exist.** Realtime does not declare it; if it is missing the log says to run `rabbitmq-setup`.
- **Validation** (service layer): `game_id` and every player ID must be UUIDs (lower-cased), names are trimmed and must not
  be blank. The game comes only from the event's `game_id`, and every query filters by it.
- **Idempotent by design:** all writes are upserts or deletes, so a redelivered event changes nothing.

## 10. Trying it

**With the real Social** (Docker stack running, two player tokens in the same game):
```bash
go run ./tools/devtoken -player p-ali    -name Ali    -game 11111111-1111-4111-8111-111111111111
go run ./tools/devtoken -player p-rashad -name Rashad -game 11111111-1111-4111-8111-111111111111
```
1. Open the two tokens as two WebSocket connections (`ws://localhost:8083/api/v1/ws?token=<token>`); each `hello` has a `playerId`.
2. Ali: `POST http://localhost:8082/api/v1/friends/requests` with `Authorization: Bearer <Ali token>` and body
   `{"playerId":"<Rashad playerId>"}` → 201. Rashad's socket gets `notification` `friend_request`.
3. Rashad: `POST http://localhost:8082/api/v1/friends/requests/<request id>/accept` (the id is in the 201 response, or in
   `GET /api/v1/friends/requests`). Both sockets get `conversation.added` and `notification` `friend_accepted`.
4. Check the row: `docker compose exec postgres psql -U postgres -d clan -c "select * from realtime.conversations"`.

**Without Social (publish by hand):** RabbitMQ dashboard → Exchanges → `socialkit.events` → Publish message. Routing key
`friend.accepted`, payload one line:
```json
{"event_id":"e-1","type":"friend.accepted","occurred_at":"2026-11-10T08:00:00Z","game_id":"11111111-1111-4111-8111-111111111111","data":{"players":[{"id":"<ALI playerId>","name":"Ali"},{"id":"<RASHAD playerId>","name":"Rashad"}]}}
```
Use straight quotes and an RFC 3339 `occurred_at`, or the log shows `dropping unusable event`. Watch **Queues → `realtime.events`**:
Ready and Unacked go back to 0 within a second, and **Consumers** lists one consumer with prefetch 20.

**Logs:** `docker compose logs -f realtime` (look for `event handled`, `dropping unusable event`) and
`docker compose logs -f social` (`publish event failed`).

**Tests:** `go test ./...`. The consumer's ack rules are tested with a fake acknowledger, the field names with
`TestDispatchReadsEventsAsSocialPublishesThem`, and the SQL in `services/realtime/internal/repository` runs against a real
Postgres when `REALTIME_TEST_DATABASE_URL` is set.

## 11. Known gaps and agreements still open

| Gap | Effect | Plan |
|---|---|---|
| Social does not send `request_id` in `friend.request_sent` | `friend_request` notification has `requestId: null` | Social adds `"request_id"`; Realtime already reads it. |
| `friend.removed` is not published | Removing a friend does not remove the DM from the lists | Social publishes it with the shape in section 5, or both sides agree on another. |
| An event can be lost: broker down at publish time, or a transient failure twice in a row at consume time | Realtime misses a friendship until restart/backfill | Transactional outbox in Social, dead-letter queue and startup backfill (TEAM-151). |
| `online` in `conversation.added` is "has an open WebSocket now" | No grace period, ignores the game's "hide online status" | Presence task (TEAM-149). |
| `data` field names are not compiler-checked | A rename on one side drops events silently (only a log line) | The contract test in section 2; keep this file in step. |

## 12. Adding a new event: checklist

1. **Name and fields:** add a section to 5 above (and a row in the README "Event format" table). Players are `{ id, name }`
   with Social IDs. Put the tenant in the envelope's `game_id`, never in `data`.
2. **Binding:** make sure `deploy/rabbitmq/definitions.json` binds the routing key to the consumer's queue
   (a new service gets its own queue).
3. **Publisher:** call `events.New(type, gameID, data)` through the `Publisher` interface **after the commit**; test it with `events.Recorder`.
4. **Consumer:** add the type constant, a `data` struct and a `case` in `dispatch.go`; the rules and DB work go in the
   service and repository (every query takes the game ID; writes are upserts or deletes).
5. **Tests:** a dispatcher test with the producer's exact shape, service tests with a fake store, a repository test against a
   real database, and a case in `TestDispatchDropsUnusableEvents` for a malformed event.
6. **Docs:** this file, `openapi/realtime-ws.md` if a new frame goes to the browser, and the README tables.
