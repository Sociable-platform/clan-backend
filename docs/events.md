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
 │   1. save the friendship (Postgres)  │   ← one transaction: the change and its event
 │   2. insert friend.accepted into     │
 │      the outbox table, COMMIT        │   ← a rollback keeps neither
 │   3. outbox relay (background):      │   ← retried until the broker confirms; a request
 │      publish, then mark it sent      │     never waits for RabbitMQ
 └──────────────────┬───────────────────┘
                    │ AMQP, persistent message, routing key = event type, publisher confirm
                    ▼
        RabbitMQ  exchange  socialkit.events  (topic, durable)
                    │  bindings: config.published, friend.*, player.*, clan.*, game.*
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
| Where Social publishes `player.blocked` / `player.unblocked` | `services/social/internal/service/blocks.go` (`BlockPlayer` also publishes the friend events a block causes; `UnblockPlayer`) |
| Where Social publishes `player.renamed` | `services/social/internal/service/players.go` (`EnsurePlayer`) and `services/social/internal/service/player_deletion.go` (`DeletePlayer`) |
| Where Social publishes the events of a player deletion | `services/social/internal/service/player_deletion.go` (`deletionEvents`): only existing types, see "Player deletion" under `player.renamed` |
| Where Social publishes clan events | `services/social/internal/service/clans.go` (leave and kick both go through `publishLeave`; `clan.renamed` from `RenameClan`); `clan.member_role_changed` in `services/social/internal/service/roles.go` |
| Where Platform publishes `config.published` | `services/platform/internal/service/widget.go` (`addWidgetVersion`), wired in `services/platform/cmd/main.go` |
| Where Platform publishes `game.suspended` / `game.resumed` | `services/platform/internal/service/admin_status.go` (`SetGameStatus`, `SetDeveloperStatus`) and `game_team.go` (`changeTeam`); Realtime handles them in `services/realtime/internal/consumer/dispatch.go` (`gameStatusChanged`) |
| Social wiring (`SOCIAL_RABBITMQ_URL`) | `services/social/cmd/main.go` |
| Exchange, queue and bindings | `deploy/rabbitmq/definitions.json`, applied by the `rabbitmq-setup` job in `docker-compose.yml` |
| Consumer loop (connect, ack, retry, reconnect) | `services/realtime/internal/consumer/consumer.go` |
| Decoding and routing by event type | `services/realtime/internal/consumer/dispatch.go` |
| Rules and DB work per event | `services/realtime/internal/service/events.go`, `services/realtime/internal/repository/conversations.go`; clan rooms in `services/realtime/internal/service/clans.go` and `services/realtime/internal/repository/clans.go` |
| Realtime wiring (`REALTIME_RABBITMQ_URL`, shared hub) | `services/realtime/cmd/main.go` |
| What the player's browser receives | `openapi/realtime-ws.md` |
| Every message, without explanation (events, then the clan chat frames) | `docs/event-messages.md` |
| Tests that pin the field names | Realtime, in `services/realtime/internal/consumer/dispatch_test.go`: `TestDispatchReadsEventsAsSocialPublishesThem` (what Social publishes) and `TestDispatchReadsEveryMessageInTheEventMessagesFile` (the agreed format). Social, in `services/social/internal/service/friend_events_test.go`: `TestFriendEventsHaveTheFieldsOfTheEventMessagesFile`, and in `clan_events_test.go`: `TestClanEventsHaveTheFieldsOfTheEventMessagesFile`, in `member_roles_test.go`: `TestClanMemberRoleChangedHasTheFieldsOfTheEventMessagesFile`, in `player_events_test.go`: `TestPlayerRenamedHasTheFieldsOfTheEventMessagesFile`, and in `blocks_test.go`: `TestBlockEventsHaveTheFieldsOfTheEventMessagesFile`. Platform, in `services/platform/internal/service/config_events_test.go`: `TestConfigPublishedHasTheFieldsOfTheEventMessagesFile` and, in `game_status_events_test.go`, `TestGameStatusEventsHaveTheFieldsOfTheEventMessagesFile` |

Realtime decodes the same `events.Event` struct that Social encodes, so a renamed envelope field breaks both sides at
compile time. The `data` fields are plain maps and are **not** checked by the compiler: that is what the two tests above are for.

## 3. The broker: exchange, queue, bindings

| Name | Kind | Notes |
|---|---|---|
| `socialkit.events` | exchange, type `topic`, durable | Every service publishes here. The routing key is the event `type`. |
| `realtime.events` | queue, durable | Realtime's own queue. Policy `realtime-events-dead-letter` sends what Realtime rejects to `socialkit.dead` with routing key `realtime.events`. |
| `socialkit.dead` | exchange, type `direct`, durable | The dead-letter exchange. Each consumer queue dead-letters with its own name as the routing key, so each gets its own dead-letter queue. |
| `realtime.events.dead` | queue, durable | Realtime's **dead-letter queue (DLQ)**: the events Realtime rejected (section 9). Policy `realtime-events-dead-ttl` keeps them 14 days (`message-ttl`), then they are dropped. |

Bindings from `socialkit.events` to `realtime.events`: `config.published`, `friend.*`, `player.*`, `clan.*`, `game.*`.
So Realtime also receives events it does not handle yet (see section 6); it acknowledges and ignores them.
Binding from `socialkit.dead` to `realtime.events.dead`: `realtime.events`.

The dead-letter settings are **policies**, not queue arguments: RabbitMQ refuses to change the arguments of a queue that
already exists, while a policy applies to it at once. So an environment made before the DLQ gets it by running
`rabbitmq-setup` again, without deleting `realtime.events` or the messages in it. `TestTheConsumerQueueDeadLettersWhatItRejects`
(in `services/realtime/internal/consumer`) checks the file, and `TestTheHelmChartHasTheSameDefinitions` that the Helm
chart's copy (`deploy/helm/clan/files/definitions.json`) is the same.

`docker compose up` runs the one-shot `rabbitmq-setup` job, which imports `deploy/rabbitmq/definitions.json` through the
management API. It adds what is missing, updates the policies, and never deletes messages. A new queue or binding is added to that file in
the same merge request as the code that needs it. A routing key that no queue is bound to is dropped by the broker.

Management dashboard: http://localhost:15672 (log in with `RABBITMQ_USER` / `RABBITMQ_PASSWORD` from `.env`).

## 4. The envelope (every event)

A message body is JSON. AMQP properties set by the publisher: `delivery_mode` 2 (persistent), `content_type`
`application/json`, `message_id` = `event_id`, `type` = event type, `timestamp` = `occurred_at`.

| Field | Type | Meaning |
|---|---|---|
| `event_id` | string (UUID v4) | Unique per message. It identifies the message, **not** the thing it is about (that is `request_id`, `clan.id`, ... inside `data`). The same event delivered twice has the same `event_id`: Realtime records it in `processed_events` and skips a copy (section 9). Realtime logs it. |
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
- A copy of the event (same `event_id`) is skipped (section 9), so the notification shows once.

### `friend.request_cancelled`

- **Published by Social** when the sender takes the request back (`DELETE /api/v1/friends/requests/{id}`, 204), or when the
  sender blocks the receiver (`POST /api/v1/players/{playerID}/block`). Published today.
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

- **Published by Social** when the receiver declines (`POST /api/v1/friends/requests/{id}/decline`, 204), or when the
  receiver blocks the sender (`POST /api/v1/players/{playerID}/block`). Published today.
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
- A copy of the event (same `event_id`) is skipped (section 9). The same accept arriving as a new event changes nothing in the
  database; its frames are sent again, and the SDK ignores a conversation id it knows.

### `friend.removed`

- **Published by Social** after `DELETE /api/v1/friends/{playerID}` (either friend, 204), or when one friend blocks the other
  (`POST /api/v1/players/{playerID}/block`; `removed_by` is the blocker). Published today (TEAM-133).

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

- **Published by Social** when a player's display name changed: the first request whose player token carries the new
  name publishes it once (`EnsurePlayer`, `services/social/internal/service/players.go`). Not on the first request of a new player.
  Also, last, when a player is deleted (`DELETE /api/v1/players/me`), with the name `Deleted player`.
- **Player deletion (TEAM-249)** has no event type of its own. In one outbox transaction Social publishes, in this order:
  `clan.member_left` with reason `left` (or `clan.disbanded` when the player was the last member), one `friend.removed`
  per friendship with `removed_by` = the deleted player, `friend.request_cancelled` for each request the player sent and
  `friend.request_declined` for each one they received, `player.unblocked` for each block by or of the player, and then
  `player.renamed`. So Realtime cleans up DMs, notifications and names with the handlers it already has.
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

- **Published by Platform** when the developer presses Publish (or rolls back): written to Platform's outbox in the
  transaction that saves the new config version (`services/platform/internal/service/widget.go`), and sent by its relay
  after the commit. A rollback saves a new version, so `version` is that new number.
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

### `game.suspended` and `game.resumed`

- **Published by Platform** when an admin suspends or resumes a game, or a whole developer, and (TEAM-308) when a change to a
  game's team changes whether the game is served: written to Platform's outbox in the transaction of the change and its audit row
  (`services/platform/internal/service/admin_status.go`, and `changeTeam` in `service/game_team.go`), and sent by its relay after
  the commit. The full messages are in [`event-messages.md`](event-messages.md).
- **`data`:** `status`, `suspended` or `active`: the game's **effective** status after the change, the same word as `status` in
  `GET /internal/v1/games/{gameID}/config`. Never the reason or the admin's note.
- **When it is published:** only when a change was saved, so a repeated request publishes nothing. A game is served when the game
  is active **and at least one of its owners has an active account** (a game has a team: owners and members; only owners count, and
  a game with one owner behaves as it did before teams), so an event goes out only when that answer flips:
  - a game-level change publishes one event for that game, unless every owner is suspended (the game was off before and stays off);
  - a developer-level change publishes one event for each game they **own** whose own switch is on and that has **no other owner with
    an active account**. A game that was suspended on its own is skipped: it is off before and after, and a `game.resumed` for it
    would let it through. Games they are only a member of, and games they share with an active owner, keep being served and publish
    nothing;
  - a **team change** (an owner leaves, is removed or demoted; a member is made an owner) publishes one event for the game when it
    flips the answer, for example the last active owner leaving a game whose other owner is suspended: `game.suspended`.
- **If the broker is down** the admin's request still succeeds; the event waits in Platform's outbox and reaches Realtime once
  the broker is back. If the event cannot be written, or the developer's status cannot be read to decide on it, the whole change
  fails and the admin can try again. The internal config says the same thing as the event, so a consumer that wants the truth
  reads `GET /internal/v1/games/{gameID}/config`.
- **Realtime does:**
  - `game.suspended`: records the game as suspended at once, then closes **every open connection of that game, and of no other**,
    with close code `4403` and reason `game_suspended` (the same hub reach as `config.updated`). A new connection is refused the same
    way, before `hello` (see `openapi/realtime-ws.md`, section 6). The event is trusted as it is: it is the latest thing Realtime knows.
  - `game.resumed`: asks Platform for the game's config (`GET /api/v1/public/games/{gameID}/widget-config`), which has the final say,
    and keeps the answer. If Platform says the game is served, new connections succeed and the config version in `hello` is the newest
    (the game may have published while it was suspended; that `config.published` had nothing to refresh and is acknowledged). If
    Platform says it is still suspended (the event was old), nothing changes. If Platform cannot be reached the event is tried once
    more, and until then the last known status stays.
  - **A lost event** is caught by the status expiring: a new connection asks Platform again after `gameconfig.Options.StatusTTL`
    (30 s). **Platform down** keeps the last status seen, and a game Realtime has never seen is served (fail open).

```json
{
  "event_id": "5e0a9c3f-2b71-4d84-9a16-7c3d8e1f0b42",
  "type": "game.suspended",
  "occurred_at": "2026-11-13T10:15:02.118Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "status": "suspended"
  }
}
```

### The clan events: `clan.created`, `clan.member_joined`, `clan.member_left`, `clan.disbanded`

- **Published by Social** (`services/social/internal/service/clans.go`); the full messages are in
  [`event-messages.md`](event-messages.md). `clan` is `{ id, name }` with Social's clan ID, `player` is `{ id, name }`.
- **Realtime does:** each clan has one chat room, a conversation of `kind` `clan` with the clan's ID in `clan_id`
  (one per game and clan).
  - `clan.created`: makes the room with the owner (`player`) as its first member and sends `conversation.added` to the owner.
  - `clan.member_joined`: adds the `player` (and makes the room if `clan.created` was never seen), sends `conversation.added` to
    the player and a `clan_joined` notification to the other members. The room comes with its **whole history**: what was written
    before the player joined, and while they were away if they were a member before. Their read position went with their
    membership, so the other members' messages count as unread. When the player was not a member yet, a `member_joined`
    member line is stored in the room and sent as `message.new` to every member, the joiner included (TEAM-291); a repeated
    event finds the membership and writes no second line.
  - `clan.member_left`: deletes the player's membership (the messages stay) and sends `conversation.removed` to that player;
    when `reason` is `kicked`, also a `clan_kicked` notification (even if Realtime never had the room). When the player was a
    member, a `member_left` line (`member_kicked` for a kick) with the name they had in the room is stored and sent as
    `message.new` to the members who stay. A `member_kicked` line also keeps `kicked_by` of the event as `kickedBy`
    (`{ id, name }`; absent when the event has none, and never on a `member_left`). `new_owner` is not needed.
  - Member lines are messages with a `kind` other than `text` and an empty body (the widget writes the sentence). They are
    in the history like any message and never count as unread (`openapi/realtime-ws.md`, "Member lines").
  - `clan.join_requested`: a player asked to join a closed clan. Sends a `clan_join_request` notification to the members the event
    names in `managers` (Social works out who may decide; Realtime knows no roles). Stores nothing, so a copy is skipped by `HandleOnce`.
  - `clan.join_request_declined`: sends a `clan_join_declined` notification to the player. An accepted request is a `clan.member_joined`.
  - `clan.disbanded`: deletes the room with its members and messages and sends `conversation.removed` and a `clan_disbanded`
    notification to everybody who was still in it. Social publishes only this event when the last member leaves, so that member is told here.
- **A copy of an event** (same `event_id`) is skipped (section 9): no rows and no frames. A new event about the same thing
  changes nothing in the database (upserts and deletes); its frames are sent again and the SDK ignores a conversation ID it
  knows. A `clan.disbanded` that comes again finds no room and sends nothing.
- A missing or malformed `clan.id` or `player` (not a UUID, no name) makes Realtime drop the event.

## 6. Events that arrive but are not handled yet

The queue is bound to `config.published`, `friend.*`, `player.*`, `clan.*` and `game.*`. Realtime **acknowledges and ignores** any type it has no
handler for (it logs them at debug level), so they never block the queue. The full messages are in
[`event-messages.md`](event-messages.md). Each is handled by its own task:

| Event | `data` | Task |
|---|---|---|
| `clan.renamed` | `clan` (the new name), `previous_name` | acknowledged with nothing to do: Realtime stores no clan names, and the room is found by the clan's ID |
| `clan.member_role_changed` | `clan`, `player`, `role`, `previous_role` (role ids of the game's `clan.roles`), `changed_by` | acknowledged with nothing to do: Realtime stores no roles. A member list frame could come later, if the SDK needs roles live (published by Social since TEAM-169) |
| `player.blocked` | `blocker_id`, `blocked_id` | Realtime stores the block in its own `blocks` table (idempotent); nothing is sent to anybody (TEAM-150). Social publishes it after `POST /api/v1/players/{playerID}/block`, only for a new block (TEAM-154). |
| `player.unblocked` | `blocker_id`, `blocked_id` | Realtime deletes the block (idempotent); nothing is sent (TEAM-150). Social publishes it after `DELETE /api/v1/players/{playerID}/block`, only when a block was removed (TEAM-154). |

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

**`conversation.added`** (to each player; `unread` is theirs, `online` = has an open WebSocket, or closed the last one less than 10 s ago):
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

**`presence.update`** (not made by an event: Realtime sends it itself; `occurred_at` is when it was decided). To the connected friends of a player,
`offline` ten seconds after their last connection closed and only if they did not connect again; `online` when their first connection opens and
they were offline (TEAM-274: not for another tab or a return within the ten seconds); and, for `friend.accepted` between two players who are both
online, `online` to each one about the other, right after `conversation.added`:
```json
{ "event_id": "…", "type": "presence.update", "occurred_at": "…", "game_id": "…", "data": { "playerId": "2c33a1b2-…", "status": "offline" } }
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
| `friend.removed` | each: `conversation.updated` (the DM, `readOnly: true`, members offline; TEAM-283) |
| `player.renamed` | nobody |
| `config.published` | every connection of the game: `config.updated` |
| `game.suspended` | no frame: every connection of the game is closed with `4403` `game_suspended` |
| `game.resumed` | nobody (the game accepts connections again) |

Chat messages are **not** events: they travel over the WebSocket only (`message.send` / `message.new`, see `openapi/realtime-ws.md`).

## 8. The publishers (Social and Platform)

- **Transactional outbox.** Every Social and Platform event is written to that service's own `outbox` table **in the
  transaction of the change it describes** (`service.change`: the store call and `AddEvents` run inside `InTx`). A
  rolled-back change leaves no event, and a committed change always has its event. Nothing talks to RabbitMQ during a request.
- **The outbox relay** (`events.Relay`, started in each service's `cmd/relay.go`) publishes the unsent rows oldest first,
  waits for the broker's confirm and only then marks a row sent. With RabbitMQ down the rows wait, the relay retries
  (1 s doubling up to 30 s) and sends them once the broker is back. Details: rabbitmq.md "Event outbox".
- Built in each service's `cmd/main.go` from `SOCIAL_RABBITMQ_URL` / `PLATFORM_RABBITMQ_URL`. Without it the relay uses
  `events.Noop{}`: rows are marked sent and dropped, and the log says the URL is not set. docker compose sets both.
- Delivery is **at least once**: a row can be sent twice (section 9 says how Realtime skips the copy).
- Order: rows go out in the order they were written. Events about the same pair or clan are written under that pair's or
  clan's lock, so they keep their order.
- Service tests read the events a fake store's transactions committed (`recorderOf(store)`); repository tests check that
  an event rolls back with its change.
- To add an event to a change: return it from the `change` function, `return []events.Event{events.New("friend.removed", gameID, data)}, nil`.

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
| **Copy** of an event handled before (its `event_id` is in `processed_events`) | `ack`, nothing stored or sent | (debug) `skipping an event handled before` |
| Type not handled yet | `ack`, ignore | (debug) `ignoring event nobody handles yet` |
| **Unusable** event: not JSON, bad `occurred_at`, missing or wrong-shaped `data`, an ID that is not a UUID, a blank name, the same player twice, `players` not exactly 2, `removed_by` not one of the two players, a missing `request_id` where it is required | rejected **without requeue** at once, to the DLQ | (warning) `event rejected to the dead-letter queue`, `reason` `unusable` |
| **Transient** failure (e.g. database down), first time | waits 1 s, then `nack` with requeue | `event failed, will try once more` (event_id, type, error) |
| Transient failure, second time (message is marked redelivered) | rejected without requeue, to the DLQ | (error) `event rejected to the dead-letter queue`, `reason` `failed_twice` |

The rejection line is the only place our reason is kept, so it is written **once per rejected event** with: `event_id` (from
the body; the AMQP `message_id` when the body cannot be read), `type`, `routing_key`, `reason` (`unusable` or
`failed_twice`), `error` (the details) and `dead_letter_queue`. To find why a dead event was rejected, search the log for its
`message_id`.

**Watching the DLQ.** While the consumer is connected it counts `realtime.events.dead` (a passive declare on a channel of
its own, so a missing queue never closes the consumer's channel) when it connects and every `REALTIME_DLQ_CHECK_INTERVAL`
(5 min). An empty queue logs nothing; otherwise one warning: `dead-letter queue holds rejected events` with `count`, or
`dead-letter queue not found, rejected events are deleted; run rabbitmq-setup`, or `could not count the dead-letter queue`.
None of them stops the consumer.

A rejected message is **not deleted**: the broker moves it to `realtime.events.dead` (section 3), with an `x-death` header
that says where it came from (`queue`), why (`reason`: `rejected`), how often (`count`) and its original routing key
(`routing-keys`); the body, `message_id` and `type` are unchanged. Our own reason for rejecting it is in the log line above,
found by `event_id`. They are kept 14 days.

**Listing and replaying dead events** (`tools/dlq`, broker address from `-url` or `REALTIME_RABBITMQ_URL`):

```bash
go run ./tools/dlq list                         # event_id, type, game, from queue, original routing key, reason, times, since
go run ./tools/dlq replay -id <event_id>,...     # send these back to the queue they came from
go run ./tools/dlq replay                       # send all of them back
```

- `list` leaves every event in the DLQ. Fix the cause first (bring the database back, fix the publisher), then replay.
- `replay` sends each event **back to the queue it was dead-lettered from** (`x-death`), through the default exchange, so
  only the consumer that rejected it sees it again; the body, `message_id` and `type` are unchanged. An event leaves the DLQ
  only after the broker confirmed its copy, and the target queue is checked first (the broker would confirm, and drop, a
  copy no queue takes), so a failure halfway loses nothing: the failed event and the ones after it stay.
- Replaying is safe: an event Realtime already handled is skipped by its `event_id` (no rows, no frames), and an event that
  is still unusable is rejected again and goes back to the DLQ.
- The management UI shows them too: **Queues > realtime.events.dead > Get messages**, with *Ack mode: Nack message requeue
  true* so they stay.

- **Reconnect:** if the broker is down or restarts, Realtime keeps serving WebSockets and reconnects with a growing wait
  (1 s, 2 s, 4 s … up to 30 s; reset after a successful connection). Log: `event consumer stopped, will reconnect`, then
  `event consumer started`. The broker password is never logged (only the host).
- **The queue must already exist.** Realtime does not declare it; if it is missing the log says to run `rabbitmq-setup`.
- **Validation** (service layer): `game_id` and every player ID must be UUIDs (lower-cased), names are trimmed and must not
  be blank. The game comes only from the event's `game_id`, and every query filters by it.
- **Delivery is at least once, and consumers must be idempotent.** RabbitMQ redelivers a message that was not acknowledged,
  and a publisher's outbox relay (rabbitmq.md "Event outbox") sends an event again if it stops after the broker confirmed it but
  before it marked it sent. Every consumer must expect the same `event_id` more than once.
- **How Realtime does it:** the friend, player and clan events run through `service.HandleOnce`. In one transaction it
  inserts `(game_id, event_id)` into `processed_events` and does the event's writes (the repository methods take that
  transaction from the context, and their own `Begin` becomes a savepoint). If the row was already there, nothing is
  stored, the copy is acknowledged and no frame is sent. If a write fails, the record rolls back with it, so the retry
  handles the event. Frames are sent only after the commit. A second copy that arrives while the first is still being
  handled waits for it on the primary key, then is skipped.
- `config.published`, `game.suspended` and `game.resumed` do not use it: they ask Platform (not inside a transaction),
  and handling them again changes nothing. Types Realtime ignores are not recorded.
- `event_id` must be a UUID; an event without one is unusable and dropped.
- The records are kept for `REALTIME_PROCESSED_EVENTS_RETENTION` (7 days) and deleted once an hour; a copy that arrives
  later would be handled again. All writes are still upserts or deletes, so even then the database does not change.

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
Use straight quotes and an RFC 3339 `occurred_at`, or the log shows `event rejected to the dead-letter queue` (`reason` `unusable`). Any other message of
[`event-messages.md`](event-messages.md) can be published the same way: the routing key is its `type`, the body is the whole
message with `game_id` and the player IDs replaced by real ones. Watch **Queues → `realtime.events`**:
Ready and Unacked go back to 0 within a second, and **Consumers** lists one consumer with prefetch 20.

**Logs:** `docker compose logs -f realtime` (look for `event handled`, `event rejected to the dead-letter queue`, `dead-letter queue holds rejected events`) and
`docker compose logs -f social` / `platform` (`outbox relay could not send`).

**Tests:** `go test ./...`. The consumer's ack rules are tested with a fake acknowledger, the field names with
`TestDispatchReadsEventsAsSocialPublishesThem` and `TestDispatchReadsEveryMessageInTheEventMessagesFile`, and the SQL in `services/realtime/internal/repository` runs against a real
Postgres when `REALTIME_TEST_DATABASE_URL` is set.

## 11. Known gaps and agreements still open

| Gap | Effect | Plan |
|---|---|---|
| An event can be lost: broker down at publish time, or a transient failure twice in a row at consume time | Realtime misses a friendship until it is backfilled | Done for friendships: start Realtime once with `REALTIME_BACKFILL=true` (endpoints.md "Realtime backfill"). It only adds, so a lost `friend.removed` is not repaired. Social and Platform now write their events through the transactional outbox (rabbitmq.md "Event outbox"), so a broker that is down no longer loses them. An event Realtime rejects at consume time now waits in the dead-letter queue `realtime.events.dead` (section 3) for 14 days instead of being deleted, and `go run ./tools/dlq replay` sends it back once the cause is fixed (section 9). |
| Events are not checked for staleness | A late `friend.accepted` that arrives after a newer `friend.removed` would bring the conversation back | Realtime keeps the `occurred_at` of the last friendship event per pair and ignores older ones (needs a column). |
| The config cache lives in the memory of the one Realtime process | After a restart it is empty and filled again on demand (`hello`, or the next `config.published`) | Fine while Realtime is one process. With several, each would need its own queue to hear the event. |
| Clan rooms are not backfilled | A clan made before Realtime ran, or whose `clan.created` / `clan.member_joined` was lost, has no room (or misses that member) until the next join; a lost `clan.member_left` leaves the player in the room | A backfill like the friendships one: needs an internal Social endpoint that lists clan members (Social has none yet), then a clan pass in `REALTIME_BACKFILL`. |
| The members who stay in a clan get only the `member_left` / `member_kicked` line when someone leaves, not a new member list | Open tabs list the old members until they reload the conversations, unless the SDK reloads them when the line arrives | A members frame, if the SDK needs more than the line. |
| `player.renamed` sends no frame | Open tabs show the old name until they reload the conversations | A frame for it, if the SDK needs it live. |
| A lost `player.blocked` (only at consume time now: Social writes it through the outbox) | The blocked player can still message the blocker | Replay it from the dead-letter queue (`go run ./tools/dlq replay`), and a backfill of the blocks: Social stores them now (TEAM-154) but has no internal endpoint that lists them, like `/internal/v1/friendships` (the friendships backfill of TEAM-151 does not cover blocks). |
| Slow mode and the rate limit are in the memory of the one Realtime process | A restart gives everybody a fresh start | Fine while Realtime is one process. |
| The profanity list is built in and the same for every game | A game can only switch the filter on or off; no spelling tricks are caught | A per-game word list in the config, if developers ask. |
| Presence ignores the game's "hide online status" | The config v1 schema has no such field, so every game shows online status | Add the field to the schema (Platform and the dashboard), then Realtime reads it from its config cache (TEAM-149 did not build it). |
| Connecting sends friends no "online" frame | A friend who comes online after you loaded `hello` shows as offline until you reconnect | Send `presence.update` `online` to friends on the first connection (not a pending grace period); the tracker already knows when that is. |
| Presence grace timers are in the memory of the one Realtime process | A restart during the 10 seconds means the friends are never told the player went offline | Fine while Realtime is one process. |
| `data` field names are not compiler-checked | A rename on one side drops events silently (only a log line) | The two contract tests in section 2; keep `event-messages.md` and this file in step. |

## 12. Adding a new event: checklist

1. **Name and fields:** add the message to `docs/event-messages.md`, a section to 5 above (and a row in rabbitmq.md "Event format" table). Players are `{ id, name }`
   with Social IDs. Put the tenant in the envelope's `game_id`, never in `data`.
2. **Binding:** make sure `deploy/rabbitmq/definitions.json` binds the routing key to the consumer's queue
   (a new service gets its own queue).
3. **Publisher:** return `events.New(type, gameID, data)` from the function given to `service.change` (Social and Platform), so it is written to the outbox in the change's transaction; test it with the events the fake store committed (`recorderOf(store)` in Social, `store.committed` in Platform).
4. **Consumer:** add the type constant, a `data` struct and a `case` in `dispatch.go` (in `handledOnce` and `store` when the
   event stores or sends something, so a copy is skipped); the rules and DB work go in the service and repository (every
   query takes the game ID and uses `r.db(ctx)`, so it joins the event's transaction; writes are upserts or deletes).
5. **Tests:** add the type to `TestDispatchReadsEveryMessageInTheEventMessagesFile`, a dispatcher test with the producer's exact shape, service tests with a fake store, a repository test against a
   real database, and a case in `TestDispatchDropsUnusableEvents` for a malformed event.
6. **Docs:** this file, `openapi/realtime-ws.md` if a new frame goes to the browser, and the tables in `rabbitmq.md` and `endpoints.md`.
