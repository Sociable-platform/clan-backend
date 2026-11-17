# Load tests

Scripts that play many players at once against the player-facing API and chat, to learn how many
concurrent players the platform holds and what gives out first. They are written for
[k6](https://k6.io): each script is a JavaScript file that k6 runs with a number of virtual players.

They test what a player's browser and a game server call. The dashboard, the admin panel and the
developer sign-in endpoints are left out: few people use them, and sign-in is rate-limited on purpose.

| Script | What it does | Status |
| --- | --- | --- |
| `smoke.js` | One player, once: widget config, player token, own profile. Proves the setup. | ready |
| `widget-open.js` | Players loading a game page: widget config, then a player token. | planned |
| `social-reads.js` | Players opening their profile, clan, members and friends. | planned |
| `clan-chat.js` | Players in one clan room over WebSocket, sending and receiving messages. | planned |
| `mixed.js` | The three together in realistic proportions. | planned |

## Before the first run

1. **Install k6.** On Windows: `winget install k6 --source winget`, then `k6 version`.
2. **Have a stack to test.** Locally: `docker compose up -d --build` (see the main README).
3. **Create the test game**, once per environment, in Studio:
   - sign in as a developer kept for load tests (not a real customer's account);
   - register a game named `Load test`;
   - open its widget editor and publish it once (the scripts need a published config);
   - create an API key and keep it for step 4.
4. **Tell the scripts where everything is.** In PowerShell, in the window you will run k6 from:

   ```powershell
   $env:LOADTEST_GAME_ID = "<the game's ID, from Your games>"
   $env:LOADTEST_API_KEY = Read-Host "API key of the test game" -MaskInput
   ```

   `Read-Host -MaskInput` keeps the key out of the command history. Never put the key in a file
   in this repo or on the k6 command line.

   For the server, also set the three addresses (the defaults are the local ports 8081 to 8083):

   ```powershell
   $env:LOADTEST_PLATFORM_URL = "https://platform.<host>"
   $env:LOADTEST_SOCIAL_URL   = "https://social.<host>"
   $env:LOADTEST_REALTIME_URL = "https://realtime.<host>"
   ```

5. **Check the setup:**

   ```powershell
   k6 run loadtest/smoke.js
   ```

   Three green checks mean the game ID, the key and the addresses are right. A red check says
   which of them to look at.

## Rules for running against the server

The server is also where the product is shown, and a load test is meant to find the point where
it stops coping.

- Run locally first. Only bring a scenario to the server once it passes locally.
- Agree a time with the team and say when you start and stop.
- Use the test game only. Never a real developer's game or key.
- Watch the server (Datadog) while it runs, and stop the test (`Ctrl+C`) if error rates climb.
- Delete the test data afterwards (below).

## Test data

Test players are numbered `lt_1`, `lt_2`, ... with the display names `Load 1`, `Load 2`, ...
The same number is the same player in every run, so runs reuse players instead of adding more.
Everything a test creates (players, clans, friendships, messages) belongs to the test game.

To clean up, an admin suspends the test game in the admin panel while it is not in use, or the
rows of that game are removed from the social and realtime schemas by whoever runs the database.
There is no delete-game endpoint yet.

## Layout

```
loadtest/
  lib/config.js    addresses, game ID and API key, read from the environment
  lib/players.js   player numbering and the player-token request
  smoke.js         the setup check
```
