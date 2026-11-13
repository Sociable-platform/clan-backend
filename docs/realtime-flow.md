# Realtime flow (UML diagrams)

How a player's actions travel through the services: who calls whom, which event or frame is sent, and where the data is saved.
The diagrams are [Mermaid](https://mermaid.js.org) text, so GitLab and the VS Code Markdown preview draw them, and they stay in git as text.
Names come from the code. If you change a flow, change the diagram in the same merge request.

| Diagram | Shows |
|---|---|
| 1 Components | the services, the database schemas, RabbitMQ and who talks to whom |
| 2 Sign in and connect | a player token, the WebSocket connection and the first frame |
| 3 Friends become a conversation | friend request, accept, and how Realtime reacts |
| 4 Sending a message | `message.send`, the message rules in order, the save and the fan-out |
| 5 Conversation list and history | the two REST reads |
| 6 Marking a conversation read | the `conversation.read` frame |
| 7 Request cancelled or declined, friend removed | the other friend events |
| 8 Conversation lifecycle | a DM as a state machine |
| 9 Frame and event envelope | the classes behind every message |
| 10 Clan rooms | a clan's chat room made from Social's clan events |
| 11 Live re-skin | a published config reaches every open connection of the game (TEAM-148) |
| 12 Presence | who is online, the 10 second grace period, and what friends are told (TEAM-149) |

Every frame the server sends has the event envelope: `event_id`, `type`, `occurred_at`, `game_id`, `data`
(see [`event-messages.md`](event-messages.md) for every message and [`events.md`](events.md) for the details).

## 1 Components

```mermaid
flowchart LR
    subgraph Browser["Player's browser"]
        SDK["SDK"]
    end
    GameServer["Game backend"]

    subgraph Backend["clan-backend"]
        Platform["Platform service :8081"]
        Social["Social service :8082"]
        subgraph Realtime["Realtime service :8083"]
            RT_HTTP["handler: REST + WebSocket"]
            RT_Hub["hub: open connections"]
            RT_Consumer["consumer + dispatcher"]
            RT_Service["service: rules"]
            RT_Repo["repository: SQL"]
            RT_Frame["frame: envelope"]
        end
    end

    subgraph Postgres["PostgreSQL, one schema per service"]
        PG_P[("platform")]
        PG_S[("social")]
        PG_R[("realtime")]
    end

    MQ{{"RabbitMQ: exchange socialkit.events, queue realtime.events"}}

    GameServer -->|"POST /players/auth + API key"| Platform
    Platform -->|"player token (JWT, 1 h)"| GameServer
    GameServer -->|"token"| SDK
    SDK -->|"friends REST + token"| Social
    SDK -->|"REST + WebSocket + token"| RT_HTTP
    RT_Consumer -->|"GET /api/v1/public/games/id/widget-config on config.published"| Platform
    RT_HTTP -->|"same call, once per game, when hello needs the version"| Platform
    RT_HTTP -->|"GET /players/me with the player's token"| Social
    Social -->|"publish after commit"| MQ
    Platform -->|"config.published"| MQ
    MQ -->|"friend.*, player.*, clan.*, config.published, game.*"| RT_Consumer
    RT_Consumer --> RT_Service
    RT_HTTP --> RT_Service
    RT_Service --> RT_Repo
    RT_Consumer --> RT_Frame
    RT_HTTP --> RT_Frame
    RT_Frame --> RT_Hub
    RT_Hub -->|"frames"| SDK
    Platform --- PG_P
    Social --- PG_S
    RT_Repo --- PG_R
```

Rules the picture hides: a service only touches its own schema, the game always comes from the verified token and never from a request
or a frame, and Realtime and Social never import each other's code.

## 2 Sign in and connect

```mermaid
sequenceDiagram
    autonumber
    participant Game as Game backend
    participant Platform
    participant SDK as Browser SDK
    participant RT as Realtime handler
    participant Social
    participant Cache as Config cache
    participant Hub
    participant Presence

    Game->>Platform: POST /api/v1/players/auth, X-API-Key, externalPlayerId, displayName
    Platform-->>Game: token (EdDSA JWT, typ player, sub, gid, name, exp 1 h)
    Game-->>SDK: token

    SDK->>RT: GET /api/v1/ws?token=... (Origin header)
    RT->>RT: check Origin against REALTIME_CORS_ORIGINS
    RT->>RT: verify the token with Platform's public key (no call to Platform)
    RT->>Social: GET /api/v1/players/me with the player's own token
    alt Social answers
        Social-->>RT: social player ID and display name
        RT-->>SDK: 101 Switching Protocols
        RT->>Cache: version of the game's config (loaded from Platform the first time, at most 1 s, else 0)
        Cache-->>RT: configVersion
        RT->>Hub: register the connection (game, social player ID)
        RT->>Presence: Connected (a pending offline timer is cancelled)
        Presence-->>RT: online friends (friends from the database, online from the hub or a running grace period)
        RT-->>SDK: frame hello (envelope, data: playerId, configVersion, onlineFriends)
    else Social cannot be reached
        RT-->>SDK: 503 social_unavailable (retry with backoff)
    end
    Note over SDK,RT: the same token and the same Social lookup identify the caller of the REST endpoints (Authorization: Bearer)
```

## 3 Friends become a conversation

```mermaid
sequenceDiagram
    autonumber
    participant A as Alice (SDK)
    participant B as Bob (SDK)
    participant Social
    participant MQ as RabbitMQ
    participant Cons as Realtime consumer
    participant Svc as Realtime service
    participant DB as Postgres (realtime)
    participant Hub

    A->>Social: POST /api/v1/friends/requests, playerId of Bob
    Social->>Social: save the request, COMMIT
    Social-)MQ: friend.request_sent (request_id, sender, receiver)
    Social-->>A: 201 request
    MQ->>Cons: deliver (prefetch 20)
    Cons->>Svc: FriendRequestSent
    Svc-->>Cons: Push for Bob only
    Cons->>Hub: frame notification friend_request
    Hub-->>B: friend_request (every open tab of Bob)
    Cons->>MQ: ack

    B->>Social: POST /api/v1/friends/requests/{id}/accept
    Social->>Social: friendship saved, COMMIT
    Social-)MQ: friend.accepted (request_id, players)
    Social-->>B: 200 friend
    MQ->>Cons: deliver
    Cons->>Svc: FriendAccepted
    Svc->>DB: UpsertDM in one transaction (conversation by dm_key, both members, last message, unread)
    DB-->>Svc: conversation id
    Svc-->>Cons: 4 Pushes
    Cons->>Hub: conversation.added for Alice and for Bob (own unread)
    Cons->>Hub: notification friend_accepted for each, naming the other
    Hub-->>A: conversation.added, then friend_accepted
    Hub-->>B: conversation.added, then friend_accepted
    Cons->>MQ: ack after the database write

    opt something goes wrong
        Note over Cons,MQ: unusable event (bad shape, bad id): rejected at once and logged
        Note over Cons,MQ: database error: wait 1 s, requeue once, then drop and log
        Note over Cons,MQ: event type nobody handles yet: acknowledged and ignored
    end
```

Publishing happens after the commit and a failed publish never fails the request. There is no dead-letter queue yet, so a dropped event is lost.

## 4 Sending a message

```mermaid
sequenceDiagram
    autonumber
    participant A1 as Alice tab 1
    participant A2 as Alice tab 2
    participant B as Bob
    participant H as Realtime handler
    participant Svc as service
    participant DB as Postgres (realtime)
    participant Hub

    A1->>H: message.send, id c-1, conversationId, body
    H->>H: 1 rate limit of the connection: 5 in 5 s (refused: error rate_limited, the third in a row also closes with 1008)
    H->>H: read the frame (missing or wrong type: error bad_frame)
    H->>Svc: SendMessage(game and sender from the connection, conversationId, body)
    Svc->>Svc: 2 trim, not empty, no NUL, conversationId is a UUID
    Svc->>DB: 3 kind and members of the conversation in this game (the sender must be one: not_member)
    Svc->>Svc: 4 the game's chat switches from the config cache (feature_disabled)
    Svc->>DB: 5 DM only: did one block the other? (blocked)
    Svc->>Svc: 6 at most the game's maxMessageLength characters (too_long)
    Svc->>Svc: 7 slow mode per player across tabs (slow_mode with retryAfterMs)
    Svc->>Svc: 8 profanity filter if on: banned whole words become ***
    Svc->>Svc: new UUIDv7 id (its time is createdAt)
    Svc->>DB: INSERT ... WHERE the sender is a member of this conversation in this game (one statement)
    alt stored
        DB-->>Svc: all members of the conversation
        Svc-->>H: message and members
        H-->>A1: message.ack (replyTo c-1, messageId, createdAt)
        H->>Hub: message.new, encoded once
        Hub-->>A1: message.new
        Hub-->>A2: message.new
        Hub-->>B: message.new (same event_id for everybody)
    else the player may not send
        Svc-->>H: not_member, feature_disabled, blocked, empty, too_long, slow_mode or bad_frame
        H-->>A1: error frame with replyTo c-1, nothing stored, nobody else told
    else our own failure
        Svc-->>H: database error (logged without the text)
        H-->>A1: error internal, nothing stored
    end
```

`occurred_at` of both the `message.ack` and the `message.new` is the time the message was stored. A member who is offline reads it from the history later.

## 5 Conversation list and history

```mermaid
sequenceDiagram
    autonumber
    participant SDK as Browser SDK
    participant H as Realtime handler
    participant Social
    participant Svc as service
    participant DB as Postgres (realtime)
    participant Hub

    SDK->>H: GET /api/v1/conversations, Authorization Bearer token
    H->>H: verify the token (401 missing_token, invalid_token, token_expired, wrong_token_type)
    H->>Social: GET /api/v1/players/me
    Social-->>H: social player ID (503 social_unavailable if it cannot answer)
    H->>Svc: Conversations(game, player)
    Svc->>DB: conversations of the player in this game, last message, unread (at most 99), members
    DB-->>Svc: rows, most recently active first
    Svc->>Hub: who is online?
    Hub-->>Svc: open WebSockets
    Svc-->>H: conversations
    H-->>SDK: 200 { conversations: [ ... ] }

    SDK->>H: GET /api/v1/conversations/{id}/messages?before=...&limit=...
    H->>H: verify the token, ask Social (as above)
    H->>Svc: Messages(game, player, id, before, limit)
    Svc->>Svc: limit 1 to 100 (400 invalid_limit), before is a UUID (400 invalid_before)
    Svc->>DB: read-only transaction: is the player a member? then the page, newest first, limit+1 rows
    alt member
        DB-->>Svc: messages
        Svc-->>H: page and hasMore
        H-->>SDK: 200 { messages: [ ... ], hasMore }
    else not a member, unknown id, another game, or not a UUID
        Svc-->>H: not_member
        H-->>SDK: 403 not_member (one answer for all, so ids cannot be probed)
    end
```

The REST answers are plain JSON. Only frames sent over the WebSocket have the event envelope.

## 6 Marking a conversation read

```mermaid
sequenceDiagram
    autonumber
    participant A as Alice tab
    participant H as Realtime handler
    participant Svc as service
    participant DB as Postgres (realtime)

    A->>H: conversation.read, id r-1, conversationId, lastMessageId
    H->>Svc: MarkRead(game and player from the connection, ids)
    Svc->>DB: one statement: is she a member, is the message in this conversation, move last_read_message_id forward only
    alt moved, or already at or past this message
        DB-->>Svc: ok
        Note over A,H: no reply on success
    else not a member
        Svc-->>H: not_member
        H-->>A: error frame not_member, replyTo r-1
    else not a message of this conversation, or not a UUID
        Svc-->>H: bad_frame
        H-->>A: error frame bad_frame, replyTo r-1
    end
    Note over A,DB: the next GET /api/v1/conversations shows unread 0. Other players' positions are not touched
```

## 7 Request cancelled or declined, friend removed

```mermaid
sequenceDiagram
    autonumber
    participant S as Sender (SDK)
    participant R as Receiver (SDK)
    participant Social
    participant MQ as RabbitMQ
    participant Cons as Realtime consumer
    participant DB as Postgres (realtime)
    participant Hub

    alt the sender takes the request back
        S->>Social: DELETE /api/v1/friends/requests/{id}
        Social-)MQ: friend.request_cancelled
        MQ->>Cons: deliver
        Cons->>Hub: notification friend_request_cancelled
        Hub-->>R: every tab of the receiver drops the request (the sender gets nothing)
    else the receiver declines
        R->>Social: POST /api/v1/friends/requests/{id}/decline
        Social-)MQ: friend.request_declined
        MQ->>Cons: deliver
        Cons->>Hub: notification friend_request_declined
        Hub-->>R: every tab of the receiver drops the request (the sender is not told)
    else either friend removes the other
        S->>Social: DELETE /api/v1/friends/{playerID}
        Social-)MQ: friend.removed (players, removed_by)
        MQ->>Cons: deliver
        Cons->>DB: delete both memberships (the conversation and its messages stay)
        Cons->>Hub: conversation.removed for both players, no notification
        Hub-->>S: conversation.removed
        Hub-->>R: conversation.removed
    end
```

## 8 Conversation lifecycle

A direct-message conversation is one row per pair of players in a game. It is never deleted; only the memberships come and go.

```mermaid
stateDiagram-v2
    [*] --> NoConversation
    NoConversation --> Active: friend.accepted creates the conversation and both memberships
    Active --> Active: message.send and conversation.read
    Active --> Hidden: friend.removed deletes both memberships
    Hidden --> Active: friend.accepted again reuses the conversation and its history
    note right of Hidden
        The row and its messages stay.
        Nobody can read or send: 403 not_member.
        It is gone from both players' lists.
    end note
    note right of Active
        Both players are members.
        Unread counts the other player's messages
        after the player's read position.
    end note
```

When the friends are reconnected, the read positions start empty again, so the old messages of the other player count as unread.

## 9 Frame and event envelope

Both are the same five fields. The frame is built in `services/realtime/internal/frame` and the event in `pkg/events`.

```mermaid
classDiagram
    class Event {
        <<queue message, pkg/events>>
        +string event_id
        +string type
        +time occurred_at
        +string game_id
        +map data
    }
    class Frame {
        <<WebSocket frame, realtime/frame>>
        +string event_id
        +string type
        +string occurred_at
        +string game_id
        +any data
    }
    class Push {
        <<service result>>
        +string GameID
        +string[] PlayerIDs
        +string Type
        +any Data
    }
    class Conversation {
        +string id
        +string kind
        +string clanId
        +ConversationMember[] members
        +Message lastMessage
        +int unread
    }
    class ConversationMember {
        +string id
        +string name
        +bool online
    }
    class Message {
        +string id
        +string senderId
        +string senderName
        +string body
        +time createdAt
    }
    Event <.. Push : the dispatcher turns an event into Pushes
    Push --> Frame : frame.Encode adds event_id, occurred_at and game_id
    Frame o-- Conversation : data of conversation.added
    Frame o-- Message : data of message.new
    Conversation "1" o-- "1..*" ConversationMember
    Conversation "1" o-- "0..1" Message : lastMessage
```

| Field | Meaning in a frame |
|---|---|
| `event_id` | a new random UUID per frame. One `message.new` sent to several players has one id |
| `occurred_at` | the time of the queue event, the time a message was stored, or the sending time for `hello`, `pong` and `error` |
| `game_id` | the game of the connection, always the one from the token |
| `data` | the payload, unchanged by the envelope |

Frames the client sends have no envelope: `{ "type", "id", "data" }`.

## 10 Clan rooms

Realtime builds a clan's chat room from Social's clan events (`services/realtime/internal/service/clans.go`). The frames are in [`event-messages.md`](event-messages.md) under "WebSocket frames". A joiner gets the whole history, also what was written while they were away; `clan.renamed` and `clan.member_role_changed` need nothing from Realtime.

```mermaid
sequenceDiagram
    autonumber
    participant O as Owner (SDK)
    participant J as Joiner (SDK)
    participant M as Other members (SDK)
    participant Social
    participant MQ as RabbitMQ
    participant RT as Realtime consumer
    participant DB as Postgres (realtime)
    participant Hub

    O->>Social: POST /api/v1/clans
    Social-)MQ: clan.created
    MQ->>RT: deliver
    RT->>DB: clan conversation (kind clan, clanId) with the owner
    RT->>Hub: conversation.added
    Hub-->>O: the clan room

    J->>Social: POST /api/v1/clans/{id}/join
    Social-)MQ: clan.member_joined
    MQ->>RT: deliver
    RT->>DB: add the member
    RT->>Hub: conversation.added for the joiner, notification clan_joined for the other members
    Hub-->>J: the clan room with its history
    Hub-->>M: clan_joined

    J->>Social: leave, or an owner kicks the player
    Social-)MQ: clan.member_left (reason left or kicked)
    MQ->>RT: deliver
    RT->>DB: remove the member
    RT->>Hub: conversation.removed, plus clan_kicked when reason is kicked
    Hub-->>J: the clan room leaves the list

    Social-)MQ: clan.disbanded
    MQ->>RT: deliver
    RT->>DB: delete the conversation (members and messages go with it)
    RT->>Hub: conversation.removed and clan_disbanded for every member
    Hub-->>M: the clan room is gone
```

Chat in a clan room is the same `message.send` flow as diagram 4: a clan room is a conversation of `kind` `clan`.

## 11 Live re-skin

The developer presses Publish; every player who has the game open sees the new look without reloading. Realtime fetches the new config
first, so the version in the frame is one it really holds. Platform publishes `config.published` after a publish or rollback is saved.

```mermaid
sequenceDiagram
    autonumber
    participant Dev as Developer (dashboard)
    participant Platform
    participant MQ as RabbitMQ
    participant Cons as Realtime consumer
    participant Cache as Config cache
    participant Hub
    participant P as Players of the game (SDK)

    Dev->>Platform: POST /api/v1/games/{gameID}/widget/publish
    Platform->>Platform: save version N, COMMIT
    Platform-)MQ: config.published (version N)
    MQ->>Cons: deliver
    Cons->>Cache: Published(game, N)
    Cache->>Platform: GET /api/v1/public/games/{gameID}/widget-config (no-cache)
    alt Platform answers with version N or newer
        Platform-->>Cache: version and config
        Cache->>Cache: swap it in (never to an older version)
        Cache-->>Cons: current version, send
        Cons->>Hub: config.updated { version } to every connection of the game
        Hub-->>P: config.updated (all tabs of all players of this game, no other game)
        P->>Platform: after 0 to 500 ms, GET the public config and redraw
        Cons->>MQ: ack
    else Platform is down, slow, or still at an older version
        Cache-->>Cons: error, the old config stays in the cache
        Note over Cons,MQ: wait 1 s, requeue once, then drop and log
    else the event is older than the cache (it came late)
        Cache-->>Cons: do not send
        Cons->>MQ: ack, nothing is sent
    end
```

A player who connects later gets the version in `hello` (`configVersion`) and compares it with the one the SDK has. The cache is loaded
lazily from Platform, once per game at a time, and kept in memory.

## 12 Presence

A player is online while one WebSocket of theirs is open. Closing the last one does not make them offline at once: Realtime waits 10 seconds,
because a page refresh closes a socket and opens another a moment later. A player in those 10 seconds still counts as online everywhere
(`hello`, the conversation list). Friends are the other members of the player's DM conversations. Connecting tells friends nothing; they read
`hello.onlineFriends`.

```mermaid
sequenceDiagram
    autonumber
    participant SDK as Browser tab of Ali
    participant RT as WebSocket handler
    participant Hub
    participant Pres as Presence tracker
    participant DB as Realtime database
    participant F as Friends of Ali (SDK)

    SDK->>RT: the last tab of Ali closes
    RT->>Hub: unregister, it was the last connection
    RT->>Pres: Disconnected(game, Ali)
    Pres->>Pres: start a 10 s timer, Ali still counts as online

    alt Ali connects again before the timer ends (page refresh)
        SDK->>RT: new connection
        RT->>Hub: register
        RT->>Pres: Connected(game, Ali)
        Pres->>Pres: cancel the timer, nobody is told anything
    else 10 s pass
        Pres->>DB: friends of Ali (other members of Ali's DM conversations, this game)
        DB-->>Pres: friend IDs
        Pres->>Hub: is Ali connected after all?
        Hub-->>Pres: no
        Pres->>Hub: presence.update { playerId: Ali, status: offline } to the friends
        Hub-->>F: frame (only friends who are connected)
    end
```

A new friendship between two online players (`friend.accepted`, diagram 3) also sends each of them `presence.update` `online` for the other, right
after `conversation.added`. The timers are in the memory of the process: they are stopped when the service shuts down (nobody is told then), and lost
with a restart.

## Which frame is sent when

| Frame | Sent to | When | Source |
|---|---|---|---|
| `hello` | the new connection | right after the upgrade | WebSocket handler |
| `pong` | the sender of the `ping` | a `ping` frame | WebSocket handler |
| `error` | the sender of a bad frame | any frame the server refuses | WebSocket handler |
| `message.ack` | the sending tab | after a message is stored | `message.send` |
| `message.new` | every tab of every member, the sender's included | after a message is stored | `message.send` |
| `notification` `friend_request` | the receiver | `friend.request_sent` | queue event |
| `notification` `friend_request_cancelled` | the receiver | `friend.request_cancelled` | queue event |
| `notification` `friend_request_declined` | the receiver | `friend.request_declined` | queue event |
| `conversation.added` | both players, each with their own `unread` | `friend.accepted` | queue event |
| `notification` `friend_accepted` | both players | `friend.accepted` | queue event |
| `conversation.removed` | both players | `friend.removed` | queue event |
| `config.updated` | every connection of the game | `config.published` | queue event, after the config is fetched |
| `presence.update` `online` | each of two online players, about the other | `friend.accepted` | queue event, right after `conversation.added` |
| `presence.update` `offline` | the connected friends of the player | 10 s after the player's last connection closed, if they did not come back | presence tracker (no event) |

## When something is down

| What is down | What happens |
|---|---|
| RabbitMQ | Social still answers (a failed publish is only logged), but the events published while the broker is down are lost: there is no outbox yet. Realtime keeps serving WebSockets and REST and reconnects to the broker with 1, 2, 4 ... up to 30 s |
| Realtime database | REST answers 500 `internal_error`, `message.send` answers an `error` frame `internal`, the consumer retries an event once and then drops it; `hello.onlineFriends` is `[]`, and when a player's 10 s end and the friends cannot be looked up, nobody is told and the player stops counting as online |
| Platform | message rules of a game whose config is not cached are the permissive defaults (chat on, 1000 characters, no slow mode, no filter); `hello` says `configVersion` 0 after a second (and for a few seconds after, without asking again); a `config.published` event is retried once and then dropped, so a missed `config.updated` is caught by `configVersion` in the next `hello`. Chat and friends are not affected |
| Social | new WebSocket connections and REST calls to Realtime get 503 `social_unavailable`. Open sockets keep working |
| Realtime itself | events wait in the queue and are handled when it is back. Frames sent meanwhile are not replayed: after a reconnect the SDK reloads the list and the history |
