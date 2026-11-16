# Event messages

## friend.request_sent

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

## friend.request_cancelled

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

## friend.request_declined

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

## friend.accepted

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

## friend.removed

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

## player.renamed

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

## clan.created

```json
{
  "event_id": "9f98fcb4-27e8-4ad0-83d5-8ac7a51b4f77",
  "type": "clan.created",
  "occurred_at": "2026-11-10T09:41:49.734Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "clan": {
      "id": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
      "name": "Wolves"
    },
    "player": {
      "id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
      "name": "Alice"
    }
  }
}
```

## clan.member_joined

```json
{
  "event_id": "24f535d8-c95d-47a9-8c56-7a8b1d8dd48a",
  "type": "clan.member_joined",
  "occurred_at": "2026-11-10T09:41:56.771Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "clan": {
      "id": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
      "name": "Wolves"
    },
    "player": {
      "id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6",
      "name": "Bob"
    }
  }
}
```

## clan.member_left

```json
{
  "event_id": "0aae941d-22d5-44f1-917d-d34af2dc3358",
  "type": "clan.member_left",
  "occurred_at": "2026-11-10T09:42:03.808Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "clan": {
      "id": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
      "name": "Wolves"
    },
    "player": {
      "id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6",
      "name": "Bob"
    },
    "reason": "left",
    "kicked_by": null,
    "new_owner": null
  }
}
```

## clan.renamed

```json
{
  "event_id": "f0b1f182-3473-44f7-8371-95d61a34faee",
  "type": "clan.renamed",
  "occurred_at": "2026-11-10T09:42:17.882Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "clan": {
      "id": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
      "name": "Grey Wolves"
    },
    "previous_name": "Wolves"
  }
}
```

## clan.disbanded

```json
{
  "event_id": "ba5244eb-80e7-402a-96f1-5f4e7ca21d4e",
  "type": "clan.disbanded",
  "occurred_at": "2026-11-10T09:42:24.919Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "clan": {
      "id": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
      "name": "Wolves"
    }
  }
}
```

## clan.member_role_changed

```json
{
  "event_id": "5d1e9c42-8a7b-4f36-9c21-3e4f5a6b7c8d",
  "type": "clan.member_role_changed",
  "occurred_at": "2026-11-11T18:20:05.114Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "clan": {
      "id": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
      "name": "Wolves"
    },
    "player": {
      "id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6",
      "name": "Bob"
    },
    "role": "elder",
    "previous_role": "member",
    "changed_by": {
      "id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
      "name": "Alice"
    }
  }
}
```

## player.blocked

```json
{
  "event_id": "2a1bf632-ba58-45a8-a64e-1bf225471176",
  "type": "player.blocked",
  "occurred_at": "2026-11-10T09:42:31.956Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "blocker_id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
    "blocked_id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6"
  }
}
```

## player.unblocked

```json
{
  "event_id": "e966fb0e-3d5d-430c-9072-b404c1fdf6ba",
  "type": "player.unblocked",
  "occurred_at": "2026-11-10T09:42:38.993Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "blocker_id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
    "blocked_id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6"
  }
}
```

## config.published

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

## game.suspended

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

## game.resumed

```json
{
  "event_id": "b7d41e68-9c02-4f35-8a7e-3d1c5f6a2e90",
  "type": "game.resumed",
  "occurred_at": "2026-11-13T10:42:51.607Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "status": "active"
  }
}
```

# WebSocket frames

## conversation.added: clan room for the owner (clan.created)

```json
{
  "event_id": "d619b1e3-43b3-493e-9599-565022c54ccf",
  "type": "conversation.added",
  "occurred_at": "2026-11-10T09:43:03.201Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "conversation": {
      "id": "0193a6b9-0000-7aaa-8bbb-ccccddddeeee",
      "kind": "clan",
      "clanId": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
      "members": [
        {
          "id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
          "name": "Alice",
          "online": true
        }
      ],
      "lastMessage": null,
      "unread": 0
    }
  }
}
```

## conversation.added: clan room for a player who joins (clan.member_joined)

```json
{
  "event_id": "9ca77afe-d074-4456-b6b7-99c3987fe0f1",
  "type": "conversation.added",
  "occurred_at": "2026-11-10T09:43:10.455Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "conversation": {
      "id": "0193a6b9-0000-7aaa-8bbb-ccccddddeeee",
      "kind": "clan",
      "clanId": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
      "members": [
        {
          "id": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
          "name": "Alice",
          "online": true
        },
        {
          "id": "2c33a1b2-c3d4-4e5f-8061-728394a5b6c7",
          "name": "Emma",
          "online": false
        },
        {
          "id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6",
          "name": "Bob",
          "online": true
        }
      ],
      "lastMessage": {
        "id": "0193a6b8-aaaa-7bbb-8ccc-ddddeeeeffff",
        "kind": "text",
        "senderId": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
        "senderName": "Alice",
        "body": "Wolves practice tonight at 8",
        "createdAt": "2026-11-10T09:30:41.207Z"
      },
      "unread": 1
    }
  }
}
```

## notification clan_joined: to the clan's members (clan.member_joined)

```json
{
  "event_id": "47c6d17b-81e7-4125-9bfe-3e60a6cdec70",
  "type": "notification",
  "occurred_at": "2026-11-10T09:43:10.455Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "kind": "clan_joined",
    "payload": {
      "clanId": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
      "player": {
        "id": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6",
        "name": "Bob"
      }
    }
  }
}
```

## conversation.removed: clan room leaves the list (clan.member_left, clan.disbanded)

```json
{
  "event_id": "1306ceca-98bf-4c82-b224-b7bafa651322",
  "type": "conversation.removed",
  "occurred_at": "2026-11-10T09:43:20.870Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "conversationId": "0193a6b9-0000-7aaa-8bbb-ccccddddeeee"
  }
}
```

## notification clan_kicked: to the removed player (clan.member_left with reason kicked)

```json
{
  "event_id": "df8d58c2-9806-4e74-a470-f64c6a15a7f5",
  "type": "notification",
  "occurred_at": "2026-11-10T09:43:20.870Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "kind": "clan_kicked",
    "payload": {
      "clanId": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091"
    }
  }
}
```

## notification clan_disbanded: to the clan's members (clan.disbanded)

```json
{
  "event_id": "a6dd3466-0bf6-4a50-be03-34c9ce23922d",
  "type": "notification",
  "occurred_at": "2026-11-10T09:43:31.002Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "kind": "clan_disbanded",
    "payload": {
      "clanId": "3b0c4d5e-6f70-4812-9a3b-4c5d6e7f8091"
    }
  }
}
```

## message.send: client to server, clan chat (frames the client sends have no envelope)

```json
{
  "type": "message.send",
  "id": "c-1",
  "data": {
    "conversationId": "0193a6b9-0000-7aaa-8bbb-ccccddddeeee",
    "body": "See you at 8"
  }
}
```

## message.ack: server to the sender

```json
{
  "event_id": "c83314c6-5d1e-46fa-90ec-fa4646ab2e93",
  "type": "message.ack",
  "occurred_at": "2026-11-10T09:45:12.314Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "replyTo": "c-1",
    "messageId": "0193a6c0-1111-7222-8333-944455556666",
    "createdAt": "2026-11-10T09:45:12.314Z"
  }
}
```

## message.new: server to every member of the clan room

```json
{
  "event_id": "d74702d5-670c-47d5-893e-fdda2eb0e9ee",
  "type": "message.new",
  "occurred_at": "2026-11-10T09:45:12.314Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "conversationId": "0193a6b9-0000-7aaa-8bbb-ccccddddeeee",
    "message": {
      "id": "0193a6c0-1111-7222-8333-944455556666",
      "kind": "text",
      "senderId": "c1a2f3b4-5d6e-4f70-8a91-b2c3d4e5f607",
      "senderName": "Alice",
      "body": "See you at 8",
      "createdAt": "2026-11-10T09:45:12.314Z"
    }
  }
}
```

## message.new: a member line in the clan room (clan.member_joined, clan.member_left)

When a player joins, Realtime stores a `member_joined` line and sends it to every member, the joiner included.
When a player leaves or is kicked, the line is `member_left` or `member_kicked` and goes to the members who stay.
`senderId` / `senderName` are the player who joined or left, and `body` is empty: the widget writes the sentence.
A member line is never unread.

```json
{
  "event_id": "5b7e0f6c-2a4d-4e1b-9c3f-8d6a1e2b4c70",
  "type": "message.new",
  "occurred_at": "2026-11-10T09:43:10.455Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "conversationId": "0193a6b9-0000-7aaa-8bbb-ccccddddeeee",
    "message": {
      "id": "0193a6bd-2222-7333-8444-555566667777",
      "kind": "member_joined",
      "senderId": "7d2f9e10-3b4c-4d5e-8f60-718293a4b5c6",
      "senderName": "Bob",
      "body": "",
      "createdAt": "2026-11-10T09:43:10.461Z"
    }
  }
}
```

## config.updated: server to every connection of the game (config.published)

```json
{
  "event_id": "5e8c7a42-91b3-4d06-a7f5-3c2e1d0b9a84",
  "type": "config.updated",
  "occurred_at": "2026-11-10T09:42:45.030Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "version": 5
  }
}
```

## presence.update: a friend came online, went offline (after the 10 s grace), or became a friend while online (friend.accepted)

Not made by an event from the queue: Realtime sends it itself. It has no event of its own, so `occurred_at` is the moment it was decided.
Sent to the player's friends who are connected (the people they share a DM with), in the player's game. Sent with `"status": "offline"` ten seconds
after the player's last connection closed, unless they connected again in that time (a page refresh sends nothing). Sent with `"status": "online"`
when the player's first connection opens and they were offline (TEAM-274); another tab, or coming back within the ten seconds, sends nothing. With
`friend.accepted`, when both players are online, each one gets `"status": "online"` for the other, right after `conversation.added`.

```json
{
  "event_id": "9b1d6c3e-2f40-4a8b-8c57-6e0a3d1f7b92",
  "type": "presence.update",
  "occurred_at": "2026-11-10T09:43:10.512Z",
  "game_id": "11111111-1111-4111-8111-111111111111",
  "data": {
    "playerId": "2c33a1b2-6b1d-4e0f-9a52-0d8c7b6a5f41",
    "status": "offline"
  }
}
```
