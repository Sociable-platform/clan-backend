# RabbitMQ events

The broker set-up, the event format, publishing and the outbox. How Realtime consumes the events is in [events.md](events.md); every message is in [event-messages.md](event-messages.md). Back to the [README](../README.md).

## RabbitMQ events

The full guide (flow diagram, every event's fields, publisher and consumer behavior, how to test, open gaps) is in [`docs/events.md`](events.md).

Services talk to each other through one topic exchange, `socialkit.events`. `docker compose up` starts RabbitMQ and then
runs the one-shot `rabbitmq-setup` job, which imports `deploy/rabbitmq/definitions.json`:

| Queue (durable) | Bound to routing keys |
|---|---|
| `realtime.events` | `config.published`, `friend.*`, `player.*`, `clan.*`, `game.*` |
| `realtime.events.dead` | `realtime.events`, on the dead-letter exchange `socialkit.dead` (direct) |

**Dead-letter queue.** An event Realtime rejects (unusable, or failed twice) is not deleted: the policy
`realtime-events-dead-letter` on `realtime.events` makes RabbitMQ move it to `socialkit.dead`, which routes it to
`realtime.events.dead`. There it keeps its body, `message_id` and `type`, plus an `x-death` header with the original routing
key, the reason and how often. The policy `realtime-events-dead-ttl` drops dead events after 14 days. Look at them in the
management UI under **Queues > realtime.events.dead > Get messages**. Every rejection is logged once as
`event rejected to the dead-letter queue`, with `event_id`, `type`, `routing_key`, `reason` (`unusable` or `failed_twice`)
and `error`: search the log for a dead event's `message_id` to find why it was rejected. While the consumer is connected it
counts the DLQ at connect and every `REALTIME_DLQ_CHECK_INTERVAL` (5 min), and logs `dead-letter queue holds rejected
events` with the count while it is not empty.

**Replaying dead events.** Once the cause is fixed, send them back with `tools/dlq` (the broker address comes from `-url`, or
`REALTIME_RABBITMQ_URL`; the password is never printed):

```bash
go run ./tools/dlq list                       # what is in realtime.events.dead; nothing is removed
go run ./tools/dlq replay -id <event_id>,...   # send these back to realtime.events
go run ./tools/dlq replay                     # send all of them back
```

Each event goes back to the queue it was dead-lettered from and leaves the DLQ only after the broker confirmed the copy, so a
failure halfway loses nothing. Replaying is safe: Realtime skips an event it already handled, and an event that is still
unusable goes back to the DLQ. Details: `docs/events.md` section 9. The settings are policies, not queue arguments, so an
existing `realtime.events` gets them by running `rabbitmq-setup` again, with its messages kept.

The queue exists before the Realtime service does, so events published today wait in it instead of being lost.
Open http://localhost:15672, go to **Queues**, and watch `realtime.events` count them.

- The job is safe to repeat: importing definitions adds what is missing, updates the policies and never deletes messages.
  To re-apply it after editing `definitions.json`: `docker compose run --rm rabbitmq-setup`.
- A new queue or binding goes into `definitions.json` in the same commit as the code that needs it.
  Copy the file to `deploy/helm/clan/files/definitions.json` too (`TestTheHelmChartHasTheSameDefinitions` fails otherwise),
  and put queue settings in a policy, not in queue arguments, so existing environments can take them.
- Without `docker compose` (for example `go run` against your own broker), import the same file in the management UI
  (**Overview > Import definitions**).
- Check by hand: in the management UI open **Exchanges > socialkit.events > Publish message**, use routing key
  `friend.accepted`, and the `realtime.events` message count goes up by one. A key no queue is bound to
  (for example `other.thing`) is dropped.

### Event format

Every message on `socialkit.events` is JSON with the same envelope (`events.Event` in `pkg/events`), and its routing
key is its `type`. Realtime decodes the same struct, so a renamed field breaks both sides at compile time.

| Field | Meaning |
|---|---|
| `event_id` | Random UUID, also the message's `message_id`. Lets a consumer notice the same event delivered twice. |
| `type` | The event name and the routing key, for example `friend.accepted` |
| `occurred_at` | When it happened, RFC 3339 in UTC |
| `game_id` | The game the event belongs to |
| `data` | Fields that depend on `type` (table below); left out when empty |

```json
{ "event_id": "3f1c0b7e-9a2d-4c51-8e0f-6b7a1d2c3e4f", "type": "friend.accepted",
  "occurred_at": "2026-11-10T08:00:00Z", "game_id": "8d4e2a10-5b7c-4f3e-9a61-2c0d7e9b4f15", "data": { } }
```

Messages are persistent (`delivery_mode` 2) with `content_type` `application/json`.

Every event with its `data` (the full messages, as they travel on the queue, are in [`docs/event-messages.md`](event-messages.md)):

| Event (routing key) | Published by | `data` |
|---|---|---|
| `friend.request_sent` | social, `POST /api/v1/friends/requests` (201) | `{ "request_id", "sender": { "id", "name" }, "receiver": { "id", "name" } }`; `request_id` is the request's own ID, so a notification can accept it |
| `friend.request_cancelled` | social, `DELETE /api/v1/friends/requests/{requestID}` (204) | `{ "request_id", "sender": { "id", "name" }, "receiver": { "id", "name" } }` |
| `friend.request_declined` | social, `POST /api/v1/friends/requests/{requestID}/decline` (204) | the same as `friend.request_cancelled` |
| `friend.accepted` | social, accept, or a request sent back (200) | `{ "request_id", "players": [ { "id", "name" }, { "id", "name" } ] }`, the smaller ID first; `request_id` is the request that was accepted (for a request sent back: the older one) |
| `friend.removed` | social, `DELETE /api/v1/friends/{playerID}` (204) | `{ "players": [ { "id", "name" }, { "id", "name" } ], "removed_by" }`, the smaller ID first; `removed_by` is the ID of the player who removed the other |
| `player.renamed` | social, the first request whose player token carries a new display name (any endpoint) | `{ "player": { "id", "name" } }` with the new name; once per rename, not on the first request |
| `clan.created` | social, `POST /api/v1/clans` (201) | `{ "clan": { "id", "name" }, "player": { "id", "name" } }`; `player` is the owner |
| `clan.member_joined` | social, `POST /api/v1/clans/{clanID}/join` (200) | `{ "clan", "player" }` |
| `clan.member_left` | social, `POST /api/v1/clans/{clanID}/leave` (204) while others remain | `{ "clan", "player", "reason": "left", "kicked_by": null, "new_owner" }`; `new_owner` is the member who took over when the owner left, otherwise `null` |
| `clan.member_left` (kick) | social, `DELETE /api/v1/clans/{clanID}/members/{playerID}` (204) | the same fields with `"reason": "kicked"`, `kicked_by` = the member who kicked `{ "id", "name" }`, `new_owner` always `null` |
| `clan.member_role_changed` | social, `POST /api/v1/clans/{clanID}/members/{playerID}/promote` or `/demote` (200) | `{ "clan", "player", "role", "previous_role", "changed_by" }`; `role` and `previous_role` are role ids of the game's `clan.roles` |
| `clan.disbanded` | social, the last member leaves (204) | `{ "clan" }`; only this event, no `clan.member_left` |
| `clan.join_requested` | social, `POST /api/v1/clans/{clanID}/requests` (201) | `{ "clan", "player", "request_id", "managers" }`; `managers` are the IDs of the members who may decide (the owner and every role with `canHandleRequests`, or `canKick` when the config does not say) |
| `clan.join_request_declined` | social, `POST /api/v1/clans/{clanID}/requests/{requestID}/decline` (204) | `{ "clan", "player", "declined_by" }`. An accepted request publishes `clan.member_joined`; taking a request back publishes nothing |
| `clan.renamed` | social, `PATCH /api/v1/clans/{clanID}` (200) when the name changed | `{ "clan": { "id", "name" }, "previous_name" }`; `clan.name` is the new name. Sending the same name publishes nothing |
| `player.blocked`, `player.unblocked` | social, `POST` / `DELETE /api/v1/players/{playerID}/block` (204), only when a block was really added or removed | `{ "blocker_id", "blocked_id" }`. A block also publishes `friend.removed` (`removed_by` = the blocker) when the two were friends, and `friend.request_cancelled` or `friend.request_declined` when one had a pending request to the other |
| `config.published` | platform, `POST /api/v1/games/{gameID}/widget/publish` and `.../widget/versions/{version}/rollback` (200); realtime then sends `config.updated` to the game's connections | `{ "version" }`, the new version number (a rollback saves a new version, so it is that number) |
| `game.suspended`, `game.resumed` | platform, `PUT /api/v1/admin/games/{gameID}/status` and `PUT /api/v1/admin/developers/{developerID}/status` (200), after the change is saved and only when it changed whether the game is served. A developer-level change publishes one event for each game they **own** that is on by its own switch and has **no other owner with an active account**; a game suspended on its own is skipped (it stays off when the developer is resumed), and so are the games they are only a member of or share with an active owner (they keep being served). The team changes of TEAM-308 publish the same events when they change whether a game is served, in the same transaction. A game-level change while its developer is suspended publishes nothing. Realtime closes every connection of the game with `4403` `game_suspended` on `game.suspended` and accepts the game again after `game.resumed` (TEAM-208) | `{ "status" }`: `suspended` or `active`, the same word as `status` in `GET /internal/v1/games/{gameID}/config`. Never the reason or the admin's note |

`id` is always the social service's player ID (a UUID, as in `conversation_members.player_id` in Realtime), and
`name` the display name at the time of the event.

**What realtime does with the friend, player and clan events** (`player.blocked` and `player.unblocked` are kept as a copy of the blocks, see `docs/events.md`; `clan.renamed` and `clan.member_role_changed` are acknowledged and need nothing, because realtime stores no clan names or roles; any other type is acknowledged and ignored):

| Event | Realtime does |
|---|---|
| `friend.request_sent` | sends a `friend_request` notification to the `receiver` (nothing is stored). `request_id` goes into the notification as `requestId`, so the SDK can accept that very request. It is optional here only so that an older event without it is still shown (`requestId: null`). |
| `friend.request_cancelled` | sends a `friend_request_cancelled` notification to the `receiver` (all open tabs) so the SDK removes the `friend_request` with the same `requestId`. `request_id` is required. The sender is not told. |
| `friend.request_declined` | sends a `friend_request_declined` notification to the `receiver` only (their other tabs drop the `friend_request`). `request_id` is required. The sender is **not** told. |
| `friend.accepted` | creates the DM conversation of the two `players` and both memberships (or reuses it), sends `conversation.added` and a `friend_accepted` notification to both. Exactly two players are required. The notification carries `requestId` (`null` when the event has no `request_id`). |
| `friend.removed` | keeps the pair's DM with both memberships and its history, and makes it read-only (TEAM-283): each player gets `conversation.updated` with `readOnly: true` and both members offline, with no notification. Nobody can send in it (`not_friends`) and no presence goes either way until a `friend.accepted` opens it again. The same for a removal and a block. Exactly two `players` are required and `removed_by` must be one of them. |
| `player.renamed` | stores the new name with the player's memberships in that game. No frame is sent; messages keep the name their sender had when writing them. |
| `clan.created` | creates the clan's chat room (a conversation of `kind` `clan` with the clan's `clanId`) with the owner (`player`) as its first member, and sends `conversation.added` to the owner. |
| `clan.member_joined` | adds the `player` to the clan's room (and creates the room if `clan.created` was never seen), sends `conversation.added` with the room to the player and a `clan_joined` notification to the other members. The room comes with its **whole history**, also the messages written before the player joined or while they were away; their read position starts at the newest message, so that history is not unread (no "New messages" line, no unread count); only messages written after they joined count. A new membership also stores a `member_joined` line (a message with `kind` `member_joined`, the player as sender, empty body) and sends it as `message.new` to every member; a repeated event writes no second line. |
| `clan.member_left` | removes the `player` from the room (the messages stay for the others, and for the player if they join again) and sends `conversation.removed` to that player; when `reason` is `kicked`, also a `clan_kicked` notification. When the player was a member, a `member_left` line (`member_kicked` for a kick) is stored and sent as `message.new` to the members who stay; a `member_kicked` line also carries `kickedBy` (`{ id, name }`), the player who removed them, from the event's `kicked_by`. |
| `clan.join_requested` | sends a `clan_join_request` notification (`{ clanId, clanName, requestId, player }`) to the members named in `managers`. Stores nothing. |
| `clan.join_request_declined` | sends a `clan_join_declined` notification (`{ clanId, clanName }`) to the player. Stores nothing. |
| `clan.disbanded` | deletes the room with its members and messages, and sends `conversation.removed` and a `clan_disbanded` notification to everybody who was still in it (Social publishes only this event when the last member leaves). |

A field Realtime needs that is missing or malformed makes it reject that event to the dead-letter queue (logged as `event rejected to the dead-letter queue` with `reason` `unusable`).
So **Social and Realtime must keep these field names in step**; `TestDispatchReadsEventsAsSocialPublishesThem` in
`services/realtime/internal/consumer` checks the shape Social publishes today, and
`TestDispatchReadsEveryMessageInTheEventMessagesFile` feeds every message of `docs/event-messages.md` to the dispatcher.

**How realtime reads the queue**

- One consumer, one event at a time, in order. Prefetch is 20, acknowledgement is manual and comes after the database write.
- An event that fails (for example the database is down) is put back **once** after one second; if it fails again it is logged
  and rejected. An unusable event (not JSON, missing field, id that is not a UUID) is logged and rejected at once.
  A rejected event is not lost: the broker moves it to the dead-letter queue `realtime.events.dead`, where it is kept 14 days.
- Delivery is at least once, so the same event can arrive twice. Realtime records the `event_id` of every friend, player
  and clan event in `processed_events` (migration `000005`), in the same transaction as the event's writes, and acknowledges
  a copy without storing or sending anything (debug log `skipping an event handled before`). A failed write rolls the record
  back too, so the retry handles the event. The records are deleted after `REALTIME_PROCESSED_EVENTS_RETENTION`. Details: `docs/events.md` section 9.
- If the broker is down or restarts, realtime keeps running and reconnects (1 s, 2 s, 4 s ... up to 30 s).
  Without a database it does not read events at all, because it would have nowhere to store them.

### Publishing from a service

- **Social and platform** write every event to their own `outbox` table in the transaction of the change (see "Event
  outbox" below), and each service's outbox relay publishes them. A request never talks to RabbitMQ, and an event is
  never lost because the broker was down: it waits in the outbox and goes out once the broker is back.
- In the service layer a change and its events go through `change`: the store call and `AddEvents` run inside `InTx`.
  Service tests read the events a fake store committed; repository tests check that an event rolls back with its change.
- **Delivery is at least once.** An event can arrive twice (the relay stopped after the broker confirmed it but before it
  marked it sent, or RabbitMQ redelivered it). Consumers must be idempotent; Realtime skips a copy by its `event_id`.
- Without `<SERVICE>_RABBITMQ_URL` a service starts anyway, logs `..._RABBITMQ_URL is not set`, and its relay drops the events.
- The publisher (`events.NewRabbitMQ`) connects on the first event, not at startup, and reconnects by itself. After a
  failed connection it waits 5 s before trying again. Every publish waits for the broker to confirm it (up to 5 s).

**The consumer side.** An event Realtime rejects (unusable, or failed twice) waits in the dead-letter queue
`realtime.events.dead` for 14 days instead of being deleted (see "RabbitMQ events"); `go run ./tools/dlq replay` sends it
back once the cause is fixed.

### Event outbox

`pkg/events` has a transactional outbox, and platform and social each have an `outbox` table (social migration `000008`,
platform `000010`; the two files are identical, `TestOutboxMigrationsAreTheSameInEveryService` checks it).

- **Writing.** `events.Enqueue(ctx, tx, e)` inserts the event into the `outbox` table inside the caller's open `pgx.Tx`,
  so the event commits or rolls back together with the change it describes. It takes a transaction, never the pool.
- **Sending.** Each service runs one relay goroutine (`events.Relay`, started in `cmd/relay.go`) when it has a database.
  It claims up to 100 unsent rows, oldest first (`FOR UPDATE SKIP LOCKED`, so two copies of a service never send the same
  row), publishes them one by one and marks a row sent only after the broker has confirmed it. It stops a batch at the
  first failure, so a later event never overtakes an earlier one, and stores that row's `attempts` and `last_error`.
- **Broker down.** Rows wait in the table; the relay tries again after `<SERVICE>_OUTBOX_INTERVAL`, doubling up to 30 s,
  and sends them once RabbitMQ is back. Requests only insert a row, so they never wait for the broker.
- **No broker URL.** The relay still runs with the no-op publisher: rows are marked sent and dropped, as events are today.
- **Cleanup.** Sent rows older than `<SERVICE>_OUTBOX_RETENTION` (7 days) are deleted, checked once an hour.
- **Shutdown.** The relay stops with the service and records the batch it was sending first (up to 5 s).
- **Order.** Rows go out in `id` order, which is the order they were inserted. Two transactions that run at the same time
  can commit in the other order, so only events written one after the other are guaranteed to stay in order; events
  about the same thing are, when the service writes them inside the transaction that holds the row lock on it (a friend pair, a clan).
- **At least once.** If the relay stops after the broker confirmed a row but before the row was marked sent, the row is
  sent again with the same `event_id`. Consumers must handle a repeated event.
- **A row that always fails** blocks the rows after it (that is what keeps the order). Its `last_error` says why; look for
  `outbox relay could not send` in the log.
