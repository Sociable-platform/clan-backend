// Smoke test: one player goes through the whole path once. It is not a load test; it proves
// that the addresses, the game ID and the API key are right before a real scenario is run.
//
//   k6 run loadtest/smoke.js
import { check, fail } from 'k6';
import http from 'k6/http';

import { config } from './lib/config.js';
import { authPlayer, bearer } from './lib/players.js';

export const options = {
  vus: 1,
  iterations: 1,
  thresholds: { checks: ['rate==1'] },
};

export default function smoke() {
  const widget = http.get(`${config.platformUrl}/api/v1/public/games/${config.gameId}/widget-config`, {
    tags: { name: 'widget_config' },
  });
  if (!check(widget, { 'the game has a published widget config (200)': (r) => r.status === 200 })) {
    fail(`widget config answered ${widget.status}: is LOADTEST_GAME_ID right, and is the widget published?`);
  }

  const auth = authPlayer(1);
  if (!check(auth, { 'the API key gets a player token (200)': (r) => r.status === 200 && !!r.json('token') })) {
    fail(`players/auth answered ${auth.status}: is LOADTEST_API_KEY a key of this game, and not revoked?`);
  }

  const me = http.get(`${config.socialUrl}/api/v1/players/me`, {
    headers: bearer(auth.json('token')),
    tags: { name: 'player_me' },
  });
  if (!check(me, { 'Social accepts the token (200)': (r) => r.status === 200 && !!r.json('id') })) {
    fail(`players/me answered ${me.status}: is LOADTEST_SOCIAL_URL right?`);
  }
}
