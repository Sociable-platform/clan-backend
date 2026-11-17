// Widget open: what happens every time a player loads a page of the game.
//
//   1. the browser asks Platform for the game's published widget config (public, no token);
//   2. the game's server asks Platform for a player token with the API key.
//
// Each virtual player is one numbered test player (lt_1, lt_2, ...) who opens the page, stays a
// few seconds and opens it again. The config is fetched in full every time, as a first visit
// does, which is the expensive case: a returning browser would send If-None-Match and get a 304.
//
//   k6 run loadtest/widget-open.js                                   the short profile
//   $env:LOADTEST_PROFILE = "steps"; k6 run loadtest/widget-open.js  10, 50, 200, 500 players
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import http from 'k6/http';

import { config } from './lib/config.js';
import { authPlayer } from './lib/players.js';
import { stages, STOP_WHEN_FAILING } from './lib/profiles.js';

export const options = {
  scenarios: {
    widget_open: { executor: 'ramping-vus', startVUs: 0, stages: stages(), gracefulRampDown: '10s' },
  },
  // The run fails (exit code 99) when a limit is crossed. p(95) is the time within which 95 of
  // 100 requests were answered: what a player with a slow request waits.
  thresholds: {
    http_req_failed: ['rate<0.01', STOP_WHEN_FAILING],
    checks: ['rate>0.99'],
    'http_req_duration{name:widget_config}': ['p(95)<300'],
    'http_req_duration{name:player_auth}': ['p(95)<500'],
  },
};

const configUrl = `${config.platformUrl}/api/v1/public/games/${config.gameId}/widget-config`;

export default function widgetOpen() {
  const widget = http.get(configUrl, { tags: { name: 'widget_config' } });
  check(widget, { 'widget config: 200': (r) => r.status === 200 });

  const auth = authPlayer(exec.vu.idInTest);
  check(auth, { 'player token: 200 with a token': (r) => r.status === 200 && !!r.json('token') });

  // The player looks at the page for 1 to 3 seconds before the next load.
  sleep(1 + Math.random() * 2);
}
