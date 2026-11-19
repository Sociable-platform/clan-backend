// Where the stack is and which game the test plays in. Everything comes from environment
// variables, so the same scripts run against a local docker compose and the server.
//
//   LOADTEST_GAME_ID       the test game's ID (copy it from Your games in Studio)   required
//   LOADTEST_API_KEY       an API key of that game                                 required
//   LOADTEST_PLATFORM_URL  default http://localhost:8081
//   LOADTEST_SOCIAL_URL    default http://localhost:8082
//   LOADTEST_REALTIME_URL  default http://localhost:8083

function url(name, fallback) {
  return (__ENV[name] || fallback).replace(/\/+$/, '');
}

function required(name) {
  const value = __ENV[name];
  if (!value) {
    throw new Error(`${name} is not set. See loadtest/README.md, "Before the first run".`);
  }
  return value;
}

export const config = {
  platformUrl: url('LOADTEST_PLATFORM_URL', 'http://localhost:8081'),
  socialUrl: url('LOADTEST_SOCIAL_URL', 'http://localhost:8082'),
  realtimeUrl: url('LOADTEST_REALTIME_URL', 'http://localhost:8083'),
  gameId: required('LOADTEST_GAME_ID'),
  apiKey: required('LOADTEST_API_KEY'),
};

export const JSON_HEADERS = { 'Content-Type': 'application/json' };
