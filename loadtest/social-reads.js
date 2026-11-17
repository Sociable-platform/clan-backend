// Social reads: what a player's widget asks for when they open it and look around.
//
//   1. who am I                      GET /players/me
//   2. my clan and my role           GET /players/me/clan
//   3. the members of my clan        GET /clans/{id}/members
//   4. my friends                    GET /friends
//   5. my pending friend requests    GET /friends/requests
//   6. one time in five, the list of clans to discover   GET /clans?limit=20
//
// Before the run, setup() fills the test game: every player is in a clan of up to 20 and has
// six friends (lib/seed.js). That takes a moment the first time and is quick afterwards.
// Above 500 virtual players (the stress profile), several of them share a test player.
//
//   k6 run loadtest/social-reads.js
//   $env:LOADTEST_PROFILE = "steps"; k6 run loadtest/social-reads.js
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import http from 'k6/http';

import { config } from './lib/config.js';
import { bearer } from './lib/players.js';
import { stages, STOP_WHEN_FAILING } from './lib/profiles.js';
import { playersFor, seedPlayers } from './lib/seed.js';

const READS = ['player_me', 'my_clan', 'clan_members', 'friends', 'friend_requests', 'clan_list'];

export const options = {
  scenarios: {
    social_reads: { executor: 'ramping-vus', startVUs: 0, stages: stages(), gracefulRampDown: '10s' },
  },
  // Seeding 500 players on a slow link takes minutes; the default limit is one.
  setupTimeout: '15m',
  thresholds: {
    http_req_failed: ['rate<0.01', STOP_WHEN_FAILING],
    checks: ['rate>0.99'],
    // One limit per kind of read, so the result names the slow one.
    ...Object.fromEntries(READS.map((name) => [`http_req_duration{name:${name}}`, ['p(95)<300']])),
  },
};

export function setup() {
  return { players: seedPlayers(playersFor(stages())) };
}

const api = `${config.socialUrl}/api/v1`;

function read(path, token, name) {
  const response = http.get(`${api}${path}`, { headers: bearer(token), tags: { name } });
  check(response, { [`${name}: 200`]: (r) => r.status === 200 });
  return response;
}

export default function socialReads({ players }) {
  const player = players[(exec.vu.idInTest - 1) % players.length];

  read('/players/me', player.token, 'player_me');
  read('/players/me/clan', player.token, 'my_clan');
  read(`/clans/${player.clanId}/members`, player.token, 'clan_members');
  read('/friends', player.token, 'friends');
  read('/friends/requests', player.token, 'friend_requests');
  if (Math.random() < 0.2) read('/clans?limit=20', player.token, 'clan_list');

  // The player reads what came back for 1 to 3 seconds before looking again.
  sleep(1 + Math.random() * 2);
}
