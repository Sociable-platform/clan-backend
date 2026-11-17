# Load tests

Scripts that play many players at once against the player-facing API and chat, to learn how many
concurrent players the platform holds and what gives out first. They are written for
[k6](https://k6.io): each script is a JavaScript file that k6 runs with a number of virtual players.

They test what a player's browser and a game server call. The dashboard, the admin panel and the
developer sign-in endpoints are left out: few people use them, and sign-in is rate-limited on purpose.

| Script | What it does | Status |
| --- | --- | --- |
| `smoke.js` | One player, once: widget config, player token, own profile. Proves the setup. | ready |
| `widget-open.js` | Players loading a game page: widget config, then a player token. | ready |
| `social-reads.js` | Players opening their profile, clan, members and friends. | ready |
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

## Running a scenario

Every scenario ramps through a profile, chosen with `LOADTEST_PROFILE`:

| Profile | Players | Length | For |
| --- | --- | --- | --- |
| `short` (default) | up to 10 | about 2 minutes | checking that a script works |
| `steps` | 10, 50, 200, then 500, two minutes each | about 11 minutes | the real measurement |
| `stress` | 1000, 2000, then 4000, one minute each | about 5 minutes | finding where it breaks, once `steps` passes |

```powershell
k6 run loadtest/widget-open.js                                    # short
$env:LOADTEST_PROFILE = "steps"; k6 run loadtest/widget-open.js   # the real one
```

A run stops by itself when more than 5% of requests fail, so a stack that is already down is not
hammered to the end. `Ctrl+C` stops it at any time.

### Reading the result

- **THRESHOLDS** at the top is the verdict: a green tick per limit, a red cross where one was crossed.
  The limits are in each script's `options.thresholds`.
- **`http_req_duration{name:...}` p(95)** is the time within which 95 of 100 requests of that kind
  were answered. Compare it with the smoke test's few milliseconds: that gap is the cost of load.
- **`http_req_failed`** is the share of requests that got an error or no answer.
- **`vus`** is how many players were active. To see at which step things turned, watch the live
  line while it runs, or the server's dashboards.

Write down, per step: players, requests per second (`http_reqs`), p(95) per request kind, and the
failure rate. The first step where a limit is crossed is the capacity figure; what the server was
short of at that moment (CPU, database connections, memory) is the thing to fix.

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

`social-reads.js` fills the game before it starts (`lib/seed.js`): every player is put in a clan
of up to 20 members (`LOADTEST_CLAN_SIZE`, never more than the game allows) named `LT <n> <xxxx>`,
and made friends with the three players numbered after them, so everyone has six friends. Seeding
only adds what is missing, so the first run of an environment takes a moment and later runs are
quick. One player is prepared per virtual player, up to 500 (`LOADTEST_MAX_PLAYERS`).

To clean up, an admin suspends the test game in the admin panel while it is not in use, or the
rows of that game are removed from the social and realtime schemas by whoever runs the database.
There is no delete-game endpoint yet.

## Layout

```
loadtest/
  lib/config.js    addresses, game ID and API key, read from the environment
  lib/players.js   player numbering and the player-token request
  lib/profiles.js  the ramp profiles (short, steps, stress) and the stop-when-failing rule
  lib/seed.js      fills the test game: players, clans, friendships
  smoke.js         the setup check
  widget-open.js   scenario 1
  social-reads.js  scenario 2
```
