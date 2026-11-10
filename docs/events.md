# Events: how services tell each other what happened

Services never call each other to say "something changed". The service that changes data **publishes an event** to
RabbitMQ after the change is saved, and every service that cares **consumes** it from its own queue. Today Social
publishes and Realtime consumes. This file is the single place that says how that works and which field names both sides
must agree on.

[`event-messages.md`](event-messages.md) lists **only the messages**, one per event, as they travel on the queue, and after them,
under "WebSocket frames", the frames of the clan chat rooms (what the browser gets and sends). This file
explains them. The examples below are copied from it, and a Realtime test reads every message of that file, so keep the two
in step.

If you change an event, change both files, the producer, the consumer and their tests in the same merge request.

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
| Where Social publishes clan events | `services/social/internal/service/clans.go` (leave and kick both go through `publishLeave`; `clan.renamed` comes with the rename task) |
| Where Platform publishes `config.published` | `services/platform/internal/service/widget.go` (`addWidgetVersion`), wired in `services/platform/cmd/main.go` |
| Social wiring (`SOCIAL_RABBITMQ_URL`) | `services/social/cmd/main.go` |
| Exchange, queue and bindings | `deploy/rabbitmq/definitions.json`, applied by the `rabbitmq-setup` job in `docker-compose.yml` |
| Consumer loop (connect, ack, retry, reconnect) | `services/realtime/internal/consumer/consumer.go` |
| Decoding and routing by event type | `services/realtime/internal/consumer/dispatch.go` |
| Rules and DB work per event | `services/realtime/internal/service/events.go`, `services/realtime/internal/repository/conversations.go` |
| Realtime wiring (`REALTIME_RABBITMQ_URL`, shared hub) | `services/realtime/cmd/main.go` |
| What the player's browser receives | `openapi/realtime-ws.md` |
| Every message, without explanation (events, then the clan chat frames) | `docs/event-messages.md` |
| Tests that pin the field names | Realtime, in `services/realtime/internal/consumer/dispatch_test.go`: `TestDispatchReadsEventsAsSocialPublishesThem` (what Social publishes) and `TestDispatchReadsEveryMessageInTheEventMessagesFile` (the agreed format). Social, in `services/social/internal/service/friend_events_test.go`: `TestFriendEventsHaveTheFieldsOfTheEventMessagesFile`, and in `clan_events_test.go`: `TestClanEventsHaveTheFieldsOfTheEventMessagesFile`. Platform, in `services/platform/internal/service/config_events_test.go`: `TestConfigPublishedHasTheFieldsOfTheEventMessagesFile` |

Realtime decodes the same `events.Event` struct that Social encodes, so a renamed envelope field breaks both sides at
compile time. The `data` fields are plain maps and are **not** checked by the compiler: that is what the two tests above are for.

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
| `event_id` | string (UUID v4) | Unique per message. It identifies the message, **not** the thing it is about (that is `request_id`, `clan.id`, ... inside `data`). Can be used to notice the same event twice. Realtime logs it. |
| `type` | string | The event name and the routing key, e.g. `friend.accepted`. |
| `occurred_at` | string, RFC 3339 UTC | When the event was made, e.g. `2026-11-10T09:41:28.623Z`. Anything else makes Realtime drop the event. |
| `game_id` | string (UUID) | The game the event belongs to. Realtime takes the tenant from here and every query filters by it. |
| `data` | object | Fields that depend on `type` (section 5). Left out when empty. |

```json
{
  "event_id": "6847effe-a146-43cf-a37e-eff6f6fd7e9f",
  "type": "friend.accepted",
  "occurred_at": "2026-11-10T09:41:28.623Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "request_id": "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d",
    "players": [
      {
        "id": "2c33a1b2-c3d4-4e5f-8061-728394a5b6c7",
        "name": "Emma"
      },
      {
        "id": "9f4bc0d1-e2f3-4a45-8b56-67788990aabb",
        "name": "Oliver"
      }
    ]
  }
}
```

Rules for `data`:
- **Players** are `{ "id", "name" }`: `id` is **Social's own player ID** (a UUID, not the game's player ID like `u_42`), `name` the
  display name at the time of the event (a snapshot). Realtime stores both in `conversation_members`.
- The same for **clans**: `{ "id", "name" }` with Social's clan ID.
- IDs are lowercase, hyphenated UUIDs. A pair of players with no direction is `"players": [player, player]`, the smaller ID first
  (Realtime does not depend on the order). A pair with a direction uses named fields (`sender`/`receiver`, `blocker_id`/`blocked_id`).
- The player an event is about is called `player`; a role is a separate field (`new_owner`, `kicked_by`).
- Field names are `snake_case`. The game is only in the envelope's `game_id`, never in `data`.

## 5. The events Realtime handles

### `friend.request_sent`

- **Published by Social** after `POST /api/v1/friends/requests` created a new pending request (HTTP 201). Published today, with `request_id` (TEAM-133).

| `data` field | Type | Required by Realtime | Meaning |
|---|---|---|---|
| `request_id` | UUID | no (optional so an older event without it still works) | The request's ID, the one used in `/friends/requests/{id}/accept`. |
| `sender` | `{ id, name }` | yes | The player who sent the request. |
| `receiver` | `{ id, name }` | yes (`id`) | The player who got it. |

```json
{
  "event_id": "6bfe80b7-7e37-4eba-80bf-85c0bcace6ee",
  "type": "friend.request_sent",
  "occurred_at": "2026-11-10T09:41:07.512Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "request_id": "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d",
    "sender": {
      "id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
      "name": "Alice"
    },
    "receiver": {
      "id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6",
      "name": "Bob"
    }
  }
}
```

- **Realtime does:** nothing is stored. It sends one `notification` frame `friend_request` to the **receiver** (all their tabs) only.
- `request_id` becomes `requestId` in the frame, so the SDK can accept that very request. An older event without it still works
  (`requestId: null`; the SDK can load `GET /api/v1/friends/requests`).
- A duplicate delivery shows the notification twice (acceptable; the SDK matches by `requestId`).

### `friend.request_cancelled`

- **Published by Social** when the sender takes the request back (`DELETE /api/v1/friends/requests/{id}`, 204). Published today.
- **`data`:** the same as `friend.request_sent`, but `request_id` is **required** (an event without it is dropped).

```json
{
  "event_id": "24ea28ac-2875-4bb7-a1d2-2b24393065ef",
  "type": "friend.request_cancelled",
  "occurred_at": "2026-11-10T09:41:14.549Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "request_id": "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d",
    "sender": {
      "id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
      "name": "Alice"
    },
    "receiver": {
      "id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6",
      "name": "Bob"
    }
  }
}
```

- **Realtime does:** sends a `notification` `friend_request_cancelled` to the **receiver** (all tabs). The SDK removes the
  `friend_request` with the same `requestId`. The sender is not told.

### `friend.request_declined`

- **Published by Social** when the receiver declines (`POST /api/v1/friends/requests/{id}/decline`, 204). Published today.
- **`data`:** the same as `friend.request_cancelled`.

```json
{
  "event_id": "4a30586e-8521-44df-9817-b2204fa560fc",
  "type": "friend.request_declined",
  "occurred_at": "2026-11-10T09:41:21.586Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "request_id": "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d",
    "sender": {
      "id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
      "name": "Alice"
    },
    "receiver": {
      "id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6",
      "name": "Bob"
    }
  }
}
```

- **Realtime does:** sends a `notification` `friend_request_declined` to the **receiver** (all tabs, so the tabs where they did not
  press the button drop the request too). The sender is **not** told.

Cancel and decline are two event types because they have different recipients; a consumer does not branch on a flag.

### `friend.accepted`

- **Published by Social** when two players become friends, by either path:
  `POST /api/v1/friends/requests/{requestID}/accept` (200), or `POST /api/v1/friends/requests` when the other player had
  already asked (200, accepted at once). Published today, with `request_id`.

| `data` field | Type | Meaning |
|---|---|---|
| `request_id` | UUID, optional | The request that was accepted (for a request sent back: the older pending one). Lets the SDK clear the `friend_request` on every tab. |
| `players` | array of exactly 2 × `{ id, name }` | The two new friends, the smaller ID first. Any other length: the event is dropped. |

```json
{
  "event_id": "6847effe-a146-43cf-a37e-eff6f6fd7e9f",
  "type": "friend.accepted",
  "occurred_at": "2026-11-10T09:41:28.623Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "request_id": "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d",
    "players": [
      {
        "id": "2c33a1b2-c3d4-4e5f-8061-728394a5b6c7",
        "name": "Emma"
      },
      {
        "id": "9f4bc0d1-e2f3-4a45-8b56-67788990aabb",
        "name": "Oliver"
      }
    ]
  }
}
```

- **Realtime does**, in one database transaction:
  1. finds or creates the DM conversation of the pair in that game, keyed by `dm_key` = `<smaller player UUID>:<larger player UUID>`
     (lower case), so the order in the event does not matter;
  2. inserts both players as members (or updates their names);
  3. reads the last message and each member's unread count.
  Then it sends to **each** player: `conversation.added` (their own `unread`), and a `notification` `friend_accepted`
  naming the **other** player and carrying `requestId` (`null` without `request_id`).
- If the players were friends before and were removed, the **same conversation comes back with its history**.
- Running it twice changes nothing in the database; the frames are sent again, and the SDK ignores a conversation id it knows.

### `friend.removed`

- **Published by Social** after `DELETE /api/v1/friends/{playerID}` (either friend, 204). Published today (TEAM-133).

| `data` field | Type | Meaning |
|---|---|---|
| `players` | array of exactly 2 × `{ id, name }` | The two former friends. Any other length: the event is dropped. |
| `removed_by` | UUID | The player who removed the other. **Must be one of the two `players`**, otherwise the event is dropped. |

```json
{
  "event_id": "18d90869-5856-48b9-844a-ab2d3b5ab5f9",
  "type": "friend.removed",
  "occurred_at": "2026-11-10T09:41:35.660Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "players": [
      {
        "id": "2c33a1b2-c3d4-4e5f-8061-728394a5b6c7",
        "name": "Emma"
      },
      {
        "id": "9f4bc0d1-e2f3-4a45-8b56-67788990aabb",
        "name": "Oliver"
      }
    ],
    "removed_by": "2c33a1b2-c3d4-4e5f-8061-728394a5b6c7"
  }
}
```

- **Realtime does:** deletes both rows in `conversation_members` of their DM. The conversation and its **messages stay**.
  Sends `conversation.removed` with the conversation id to both players, and **no notification**: the conversation just
  disappears. If the game has no DM for the pair (the accept was never seen), it does nothing.
- Realtime used to expect `{ "a_id", "b_id" }`. That shape is now dropped as unusable.

### `player.renamed`

- **Published by Social** when a player's display name changed. **Not published yet.**
- **`data`:** `player` = `{ id, name }` with the **new** name.

```json
{
  "event_id": "dd8baa14-ed04-4ddb-9f45-d19bde912e9a",
  "type": "player.renamed",
  "occurred_at": "2026-11-10T09:41:42.697Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "player": {
      "id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
      "name": "Alice Brown"
    }
  }
}
```

- **Realtime does:** stores the new name with that player's memberships **in that game** (`conversation_members.display_name`).
  No frame is sent: the SDK sees the new name the next time it loads the conversations. Messages keep the name their sender
  had when they wrote them (`sender_name`). A redelivered event changes nothing.

### `config.published`

- **Published by Platform** after the developer presses Publish (or rolls back) and the new config version is saved
  (`services/platform/internal/service/widget.go`). A rollback saves a new version, so `version` is that new number.
- **`data`:** `version` = the new version number, a whole number from 1. The config itself is **not** in the event.

```json
{
  "event_id": "32b58d15-703e-46aa-a5ef-1d84a31b6a48",
  "type": "config.published",
  "occurred_at": "2026-11-10T09:42:45.030Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "version": 5
  }
}
```

- **Realtime does:** asks Platform for the game's newest published config (`GET /api/v1/public/games/{gameID}/widget-config`,
  `REALTIME_PLATFORM_URL`), puts it in the in-memory config cache of that game, and sends `config.updated` with `{ version }`
  to **every open connection of that game** (and of no other game). The frame names the version the cache now holds, which can be
  newer than the event's when two publishes came quickly.
- **If Platform fails** (down, too slow, an error), or does not have that version yet: the old config stays in the cache, nothing is
  sent, and the event is tried once more after a second (see section 9); after that it is dropped and logged.
- **A late event** (its version is older than the one the cache holds, so the players were told about a newer one already) is
  acknowledged and sends nothing. **A redelivered event** sends the frame again; the SDK just fetches the same config again.
- The cache is filled lazily too: the first `hello` of a game (or the first message rule that needs it) loads the config, and
  every later call reads memory. `hello` carries the cached `configVersion`.

## 6. Events that arrive but are not handled yet

The queue is bound to `config.published`, `friend.*`, `player.*` and `clan.*`. Realtime **acknowledges and ignores** any type it has no
handler for (it logs them at debug level), so they never block the queue. The full messages are in
[`event-messages.md`](event-messages.md). Each is handled by its own task:

| Event | `data` | Task |
|---|---|---|
| `clan.created` | `clan`, `player` (the owner) | clan chat rooms (TEAM-147) |
| `clan.member_joined` | `clan`, `player` | clan chat rooms (TEAM-147) |
| `clan.member_left` | `clan`, `player`, `reason` (`left` or `kicked`), `kicked_by` (player or `null`), `new_owner` (player or `null`) | clan chat rooms (TEAM-147) |
| `clan.renamed` | `clan` (the new name), `previous_name` | clan chat rooms (TEAM-147) |
| `clan.disbanded` | `clan` (only this event is published when the last member leaves) | clan chat rooms (TEAM-147) |
| `player.blocked`, `player.unblocked` | `blocker_id`, `blocked_id` | blocks sync (TEAM-151, TEAM-154) |

## 7. What the player's browser receives

Frames the server sends have the **same envelope as an event on the queue** (`event_id`, `type`, `occurred_at`, `game_id`, `data`), built in
`services/realtime/internal/frame`. `occurred_at` is the time of the queue event for the frames below, `game_id` is the game of the event, and
`event_id` is new for each frame. The examples below show `type` and `data`, with the envelope fields shortened to `…`. Frames the client
sends stay `{ "type", "id", "data" }`. Full contract: `openapi/realtime-ws.md`. The frames of the
clan chat rooms (`conversation.added` for a clan, `clan_joined`, `clan_kicked`, `clan_disbanded`, and the chat frames in a clan room) are
in the "WebSocket frames" part of [`event-messages.md`](event-messages.md); `TestFrameTemplatesInTheEventMessagesFile` checks their fields.

**`notification` / `friend_request`** (to the receiver of the request):
```json
{ "event_id": "…", "type": "notification", "occurred_at": "…", "game_id": "…", "data": { "kind": "friend_request",
  "payload": { "requestId": "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d", "from": { "id": "c1a2f3b4-…", "name": "Alice" } } } }
```

**`notification` / `friend_request_cancelled`** and **`friend_request_declined`** (to the receiver, all tabs; same payload):
```json
{ "event_id": "…", "type": "notification", "occurred_at": "…", "game_id": "…", "data": { "kind": "friend_request_cancelled",
  "payload": { "requestId": "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d", "from": { "id": "c1a2f3b4-…", "name": "Alice" } } } }
```

**`notification` / `friend_accepted`** (to each player; `player` is the other one, `requestId` the accepted request or `null`):
```json
{ "event_id": "…", "type": "notification", "occurred_at": "…", "game_id": "…", "data": { "kind": "friend_accepted",
  "payload": { "player": { "id": "7d2f9e10-…", "name": "Bob" }, "requestId": "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d" } } }
```

**`conversation.added`** (to each player; `unread` is theirs, `online` = has an open WebSocket right now):
```json
{ "event_id": "…", "type": "conversation.added", "occurred_at": "…", "game_id": "…", "data": { "conversation": {
  "id": "128cd7f6-…", "kind": "dm", "clanId": null,
  "members": [ { "id": "9f4bc0d1-…", "name": "Oliver", "online": true }, { "id": "2c33a1b2-…", "name": "Emma", "online": true } ],
  "lastMessage": null, "unread": 0 } } }
```

**`conversation.removed`** (to both players):
```json
{ "event_id": "…", "type": "conversation.removed", "occurred_at": "…", "game_id": "…", "data": { "conversationId": "128cd7f6-…" } }
```

**`config.updated`** (to every open connection of the game; `version` is the version of the config in the cache now). The SDK waits a
random 0 to 500 ms, then fetches the public config again and redraws:
```json
{ "event_id": "…", "type": "config.updated", "occurred_at": "…", "game_id": "…", "data": { "version": 5 } }
```

Who gets what:

| Event | Frames |
|---|---|
| `friend.request_sent` | receiver: `friend_request` |
| `friend.request_cancelled` | receiver: `friend_request_cancelled` |
| `friend.request_declined` | receiver: `friend_request_declined` (the sender gets nothing) |
| `friend.accepted` | both, in this order: `conversation.added`, then `friend_accepted` |
| `friend.removed` | both: `conversation.removed` |
| `player.renamed` | nobody |
| `config.published` | every connection of the game: `config.updated` |

Chat messages are **not** events: they travel over the WebSocket only (`message.send` / `message.new`, see `openapi/realtime-ws.md`).

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
| **Unusable** event: not JSON, bad `occurred_at`, missing or wrong-shaped `data`, an ID that is not a UUID, a blank name, the same player twice, `players` not exactly 2, `removed_by` not one of the two players, a missing `request_id` where it is required | rejected **without requeue** at once | `dropping unusable event` + the reason |
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
go run ./tools/devtoken -player p-alice  -name Alice  -game 11111111-1111-4111-8111-111111111111
go run ./tools/devtoken -player p-bob    -name Bob    -game 11111111-1111-4111-8111-111111111111
```
1. Open the two tokens as two WebSocket connections (`ws://localhost:8083/api/v1/ws?token=<token>`); each `hello` has a `playerId`.
2. Alice: `POST http://localhost:8082/api/v1/friends/requests` with `Authorization: Bearer <Alice token>` and body
   `{"playerId":"<Bob playerId>"}` → 201. Bob's socket gets `notification` `friend_request`.
3. Bob: `POST http://localhost:8082/api/v1/friends/requests/<request id>/accept` (the id is in the 201 response, or in
   `GET /api/v1/friends/requests`). Both sockets get `conversation.added` and `notification` `friend_accepted`.
4. Check the row: `docker compose exec postgres psql -U postgres -d clan -c "select * from realtime.conversations"`.

**Without Social (publish by hand):** RabbitMQ dashboard → Exchanges → `socialkit.events` → Publish message. Routing key
`friend.accepted`, payload one line:
```json
{"event_id":"e-1","type":"friend.accepted","occurred_at":"2026-11-10T08:00:00Z","game_id":"11111111-1111-4111-8111-111111111111","data":{"request_id":"9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d","players":[{"id":"<ALICE playerId>","name":"Alice"},{"id":"<BOB playerId>","name":"Bob"}]}}
```
Use straight quotes and an RFC 3339 `occurred_at`, or the log shows `dropping unusable event`. Any other message of
[`event-messages.md`](event-messages.md) can be published the same way: the routing key is its `type`, the body is the whole
message with `game_id` and the player IDs replaced by real ones. Watch **Queues → `realtime.events`**:
Ready and Unacked go back to 0 within a second, and **Consumers** lists one consumer with prefetch 20.

**Logs:** `docker compose logs -f realtime` (look for `event handled`, `dropping unusable event`) and
`docker compose logs -f social` (`publish event failed`).

**Tests:** `go test ./...`. The consumer's ack rules are tested with a fake acknowledger, the field names with
`TestDispatchReadsEventsAsSocialPublishesThem` and `TestDispatchReadsEveryMessageInTheEventMessagesFile`, and the SQL in `services/realtime/internal/repository` runs against a real
Postgres when `REALTIME_TEST_DATABASE_URL` is set.

## 11. Known gaps and agreements still open

| Gap | Effect | Plan |
|---|---|---|
| Social does not publish `player.renamed` | Names stored in Realtime are never refreshed | Social publishes it (shape in section 5). Realtime already handles it. |
| An event can be lost: broker down at publish time, or a transient failure twice in a row at consume time | Realtime misses a friendship until restart/backfill | Transactional outbox in Social, dead-letter queue and startup backfill (TEAM-151). |
| Events are not checked for staleness | A late `friend.accepted` that arrives after a newer `friend.removed` would bring the conversation back | Realtime keeps the `occurred_at` of the last friendship event per pair and ignores older ones (needs a column). |
| The config cache lives in the memory of the one Realtime process | After a restart it is empty and filled again on demand (`hello`, or the next `config.published`) | Fine while Realtime is one process. With several, each would need its own queue to hear the event. |
| `player.renamed` sends no frame | Open tabs show the old name until they reload the conversations | A frame for it, if the SDK needs it live. |
| `online` in `conversation.added` is "has an open WebSocket now" | No grace period, ignores the game's "hide online status" | Presence task (TEAM-149). |
| `data` field names are not compiler-checked | A rename on one side drops events silently (only a log line) | The two contract tests in section 2; keep `event-messages.md` and this file in step. |

## 12. Adding a new event: checklist

1. **Name and fields:** add the message to `docs/event-messages.md`, a section to 5 above (and a row in the README "Event format" table). Players are `{ id, name }`
   with Social IDs. Put the tenant in the envelope's `game_id`, never in `data`.
2. **Binding:** make sure `deploy/rabbitmq/definitions.json` binds the routing key to the consumer's queue
   (a new service gets its own queue).
3. **Publisher:** call `events.New(type, gameID, data)` through the `Publisher` interface **after the commit**; test it with `events.Recorder`.
4. **Consumer:** add the type constant, a `data` struct and a `case` in `dispatch.go`; the rules and DB work go in the
   service and repository (every query takes the game ID; writes are upserts or deletes).
5. **Tests:** add the type to `TestDispatchReadsEveryMessageInTheEventMessagesFile`, a dispatcher test with the producer's exact shape, service tests with a fake store, a repository test against a
   real database, and a case in `TestDispatchDropsUnusableEvents` for a malformed event.
6. **Docs:** this file, `openapi/realtime-ws.md` if a new frame goes to the browser, and the README tables.
