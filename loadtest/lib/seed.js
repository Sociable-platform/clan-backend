import http from 'k6/http';

import { config, JSON_HEADERS } from './config.js';
import { authPlayer, bearer } from './players.js';

// How the test game is filled before a scenario that reads social data:
//
//   - every test player exists in Social (their profile is created on first use);
//   - every player is in a clan of up to LOADTEST_CLAN_SIZE members (default 20, and never more
//     than the game's own limit);
//   - every player is friends with the three players numbered after them, so has six friends.
//
// Seeding is safe to repeat: a player who is already in a clan stays there, and friendships that
// exist are left alone. A second run only adds what is missing, so it is quick.

const CLAN_SIZE = Number(__ENV.LOADTEST_CLAN_SIZE || 20);
const FRIENDS_AHEAD = 3;

function social(path) {
  return `${config.socialUrl}/api/v1${path}`;
}

// While seeding, "not in a clan yet" (404) and "already friends" (409) are answers, not failures,
// so they must not count towards the run's failed-request rate.
const SEED_ANSWERS = http.expectedStatuses(200, 201, 404, 409);

function post(path, token, body = null) {
  return http.post(social(path), body ? JSON.stringify(body) : null, {
    headers: { ...JSON_HEADERS, ...bearer(token) },
    tags: { name: 'seed' },
    responseCallback: SEED_ANSWERS,
  });
}

function get(path, token) {
  return http.get(social(path), { headers: bearer(token), tags: { name: 'seed' }, responseCallback: SEED_ANSWERS });
}

function mustBe(response, statuses, what) {
  if (!statuses.includes(response.status)) {
    throw new Error(`seeding: ${what} answered ${response.status} ${response.body}`);
  }
  return response;
}

/** Signs every player in and makes sure their Social profile exists. */
function signIn(count) {
  const players = [];
  for (let number = 1; number <= count; number += 1) {
    const token = mustBe(authPlayer(number, { name: 'seed' }), [200], `players/auth for lt_${number}`).json('token');
    const me = mustBe(get('/players/me', token), [200], `players/me for lt_${number}`);
    players.push({ number, token, id: me.json('id'), clanId: null });
  }
  return players;
}

/** Puts every player who has no clan into one: fills the open clan, then starts the next. */
function joinClans(players) {
  let open = null; // { id, free }: the clan still taking members
  for (const player of players) {
    const mine = mustBe(get('/players/me/clan', player.token), [200, 404], 'players/me/clan');
    if (mine.status === 200) {
      player.clanId = mine.json('clan.id');
      continue;
    }

    if (open && open.free > 0) {
      const joined = mustBe(post(`/clans/${open.id}/join`, player.token), [200, 201, 409], 'joining a clan');
      if (joined.status !== 409) {
        player.clanId = open.id;
        open.free -= 1;
        continue;
      }
      open = null; // full (the game allows fewer members than expected): start a new clan
    }

    // The name must be unique in the game, so it carries the player's number and a random tail.
    const name = `LT ${player.number} ${Math.random().toString(36).slice(2, 6)}`;
    const created = mustBe(post('/clans', player.token, { name, joinPolicy: 'open' }), [201], `creating clan "${name}"`);
    player.clanId = created.json('id');
    open = { id: player.clanId, free: Math.min(CLAN_SIZE, created.json('maxMembers')) - 1 };
  }
}

/** Each player asks the next three to be friends; then everyone accepts what they were sent. */
function makeFriends(players) {
  if (players.length < 2) return;
  for (let index = 0; index < players.length; index += 1) {
    for (let ahead = 1; ahead <= FRIENDS_AHEAD; ahead += 1) {
      const other = players[(index + ahead) % players.length];
      if (other.id === players[index].id) continue;
      // 201 a new request, 200 accepted at once (they had asked first), 409 already friends or asked.
      mustBe(post('/friends/requests', players[index].token, { playerId: other.id }), [200, 201, 409], 'a friend request');
    }
  }
  for (const player of players) {
    const requests = mustBe(get('/friends/requests', player.token), [200], 'friends/requests');
    for (const request of requests.json('incoming') || []) {
      mustBe(post(`/friends/requests/${request.id}/accept`, player.token), [200, 404, 409], 'accepting a friend request');
    }
  }
}

/**
 * Prepares `count` test players with clans and friends and returns them as
 * `[{ number, token, id, clanId }]`. Call it from a scenario's `setup()`; what it returns is
 * handed to every virtual player. Tokens last an hour, longer than any profile.
 */
export function seedPlayers(count) {
  const players = signIn(count);
  joinClans(players);
  makeFriends(players);
  return players;
}

/** How many players a profile needs: one per virtual player, up to LOADTEST_MAX_PLAYERS (default 500). */
export function playersFor(stages) {
  const most = Math.max(...stages.map((stage) => stage.target));
  return Math.min(most, Number(__ENV.LOADTEST_MAX_PLAYERS || 500));
}
