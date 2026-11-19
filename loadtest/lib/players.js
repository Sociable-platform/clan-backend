import http from 'k6/http';

import { config, JSON_HEADERS } from './config.js';

// Test players are numbered: lt_1, lt_2, ... The same number is the same player in every run,
// so a second run reuses the players of the first instead of adding new ones.
export function playerIdentity(number) {
  return { externalPlayerId: `lt_${number}`, displayName: `Load ${number}` };
}

/**
 * Asks Platform for a player token, the way a game server does: the API key in X-API-Key and the
 * player in the body. Returns the HTTP response; the token is `response.json('token')`.
 * `tags` name the request in the results (for example { name: 'player_auth' }).
 */
export function authPlayer(number, tags = { name: 'player_auth' }) {
  return http.post(`${config.platformUrl}/api/v1/players/auth`, JSON.stringify(playerIdentity(number)), {
    headers: { ...JSON_HEADERS, 'X-API-Key': config.apiKey },
    tags,
  });
}

/** The Authorization header a player's browser sends to Social and Realtime. */
export function bearer(token) {
  return { Authorization: `Bearer ${token}` };
}
