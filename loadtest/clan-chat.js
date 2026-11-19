// Clan chat: players sitting in their clan's chat room over a WebSocket, talking.
//
// Each virtual player is a test player in a clan of up to 20 (lib/seed.js fills the game first):
//
//   1. asks Realtime for their conversations and finds the clan room;
//   2. opens the WebSocket and waits for hello;
//   3. writes a message into the room every 8 to 12 seconds for about a minute;
//   4. closes the socket and starts over, as a player who leaves the page and comes back.
//
// What is measured:
//
//   chat_delivery_ms   from one player sending a message to another player's socket receiving it.
//                      This is what players feel as "chat lag". Every message carries its send
//                      time as its text, and all players run on this machine's clock.
//   chat_ack_ms        from sending a message to the server's acknowledgement that it is stored.
//   chat_send_failed   the share of messages the server refused or never acknowledged.
//   chat_connect_failed  the share of sockets that did not open or got no hello.
//
// The test game must have clan chat on and slow mode off (the defaults), or sends are refused.
//
//   k6 run loadtest/clan-chat.js
//   $env:LOADTEST_PROFILE = "steps"; k6 run loadtest/clan-chat.js
import { check, fail } from 'k6';
import exec from 'k6/execution';
import http from 'k6/http';
import { Counter, Rate, Trend } from 'k6/metrics';
import ws from 'k6/ws';

import { config } from './lib/config.js';
import { bearer } from './lib/players.js';
import { stages, STOP_WHEN_FAILING } from './lib/profiles.js';
import { playersFor, seedPlayers } from './lib/seed.js';

const SESSION_MS = 60000; // how long a player stays connected
const SEND_EVERY_MS = [8000, 12000]; // a message every 8 to 12 seconds
const ACK_TIMEOUT_MS = 5000; // an answer later than this counts as a failed send

const deliveryMs = new Trend('chat_delivery_ms', true);
const ackMs = new Trend('chat_ack_ms', true);
const sendFailed = new Rate('chat_send_failed');
const connectFailed = new Rate('chat_connect_failed');
const sent = new Counter('chat_messages_sent');
const received = new Counter('chat_messages_received');
const refused = new Counter('chat_errors'); // error frames, tagged with their code

export const options = {
  scenarios: {
    clan_chat: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: stages(),
      // A player finishes the minute they are in before the run ends.
      gracefulRampDown: '70s',
      gracefulStop: '70s',
    },
  },
  setupTimeout: '15m',
  thresholds: {
    http_req_failed: ['rate<0.01', STOP_WHEN_FAILING],
    chat_connect_failed: ['rate<0.01'],
    chat_send_failed: ['rate<0.01'],
    chat_delivery_ms: ['p(95)<500'],
    chat_ack_ms: ['p(95)<500'],
    'http_req_duration{name:conversations}': ['p(95)<300'],
  },
};

export function setup() {
  return { players: seedPlayers(playersFor(stages())) };
}

// http://host -> ws://host and https://host -> wss://host
const socketUrl = `${config.realtimeUrl.replace(/^http/, 'ws')}/api/v1/ws`;

function between([low, high]) {
  return low + Math.random() * (high - low);
}

/** The ID of the player's clan chat room, from Realtime's conversation list. */
function clanRoom(player) {
  const response = http.get(`${config.realtimeUrl}/api/v1/conversations`, {
    headers: bearer(player.token),
    tags: { name: 'conversations' },
  });
  if (!check(response, { 'conversations: 200': (r) => r.status === 200 })) return null;
  const room = (response.json('conversations') || []).find((c) => c.kind === 'clan' && c.clanId === player.clanId);
  return room ? room.id : null;
}

export default function clanChat({ players }) {
  const player = players[(exec.vu.idInTest - 1) % players.length];
  const roomId = clanRoom(player);
  if (!roomId) {
    // Realtime builds a clan's room from Social's events. No room means that link is not working.
    fail(`lt_${player.number} is in clan ${player.clanId} but Realtime lists no room for it`);
  }

  const waiting = new Map(); // frame id -> when it was sent, until it is acknowledged
  let nextId = 0;
  let saidHello = false;

  const response = ws.connect(`${socketUrl}?token=${player.token}`, { tags: { name: 'chat_socket' } }, (socket) => {
    const send = () => {
      const now = Date.now();
      // A send that was never answered has failed.
      for (const [id, at] of waiting) {
        if (now - at > ACK_TIMEOUT_MS) {
          waiting.delete(id);
          sendFailed.add(true);
        }
      }
      const id = `m-${(nextId += 1)}`;
      waiting.set(id, now);
      // The text is the send time, so whoever receives it can tell how long it took.
      socket.send(JSON.stringify({ type: 'message.send', id, data: { conversationId: roomId, body: `t${now}` } }));
      sent.add(1);
      socket.setTimeout(send, between(SEND_EVERY_MS));
    };

    socket.on('message', (text) => {
      const frame = JSON.parse(text);
      const data = frame.data || {};

      if (frame.type === 'hello') {
        saidHello = true;
        // Players do not all start talking in the same instant.
        socket.setTimeout(send, between([500, SEND_EVERY_MS[0]]));
      } else if (frame.type === 'message.ack' && waiting.has(data.replyTo)) {
        ackMs.add(Date.now() - waiting.get(data.replyTo));
        waiting.delete(data.replyTo);
        sendFailed.add(false);
      } else if (frame.type === 'error') {
        refused.add(1, { code: data.code });
        if (waiting.delete(data.replyTo)) sendFailed.add(true);
      } else if (frame.type === 'message.new' && data.conversationId === roomId) {
        const body = (data.message && data.message.kind === 'text' && data.message.body) || '';
        const sentAt = /^t\d{13}$/.test(body) ? Number(body.slice(1)) : 0;
        if (sentAt) {
          deliveryMs.add(Date.now() - sentAt);
          received.add(1);
        }
      }
    });

    socket.setTimeout(() => socket.close(), SESSION_MS);
  });

  const opened = check(response, { 'socket: opened (101)': (r) => r && r.status === 101 });
  connectFailed.add(!(opened && saidHello));
}
