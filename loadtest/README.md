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
| `clan-chat.js` | Players in their clan's chat room over WebSocket, sending and receiving messages. | ready |
| `mixed.js` | The three together, in the proportions a live game would see. | ready |

## Before the first run

1. **Install k6.** On Windows: `winget install k6 --source winget`, then `k6 version`.
2. **Have a stack to test.** Locally: `docker compose up -d --build` (see the main README).
3. **Prepare the test game and the settings** with the setup tool (below), or by hand (after it).

### With the setup tool

`tools/loadtest-setup` does in one run what step 3 by hand does in Studio: it signs in, finds or
registers the game `Load test`, publishes its widget config with clans and clan chat on (clans of
up to 20, no slow mode), creates a fresh API key and revokes the game's older ones. It writes the
addresses, the game ID and the key into a file in your home folder, outside the repository, that
only you can read. Run it from the `clan-backend` folder.

On the local stack, where email verification is off, it registers a throwaway developer
(`loadtest-<number>@example.com`) with a random password and keeps that sign-in in the same file,
so the next run reuses the same developer, game and players:

```powershell
go run ./tools/loadtest-setup
. ~\.clan-loadtest\localhost-8081.ps1     # loads the settings into this window
k6 run loadtest/smoke.js
```

On the server, email verification is on, so no developer can be registered from a script. Register
a developer kept for load tests in Studio once (not a real customer's account), then:

```powershell
$env:LOADTEST_DEV_PASSWORD = Read-Host "Load-test developer password" -MaskInput
go run ./tools/loadtest-setup -platform https://platform.<host> -email <that developer's address>
. ~\.clan-loadtest\platform.<host>.ps1
```

The Social and Realtime addresses follow from the Platform one (`platform.` becomes `social.` and
`realtime.`; port 8081 becomes 8082 and 8083); give `-social` and `-realtime` otherwise. The
password is never a flag. A real developer's password is never written to the file; only the
throwaway one's is. If PowerShell refuses to load the file, run
`Set-ExecutionPolicy -Scope Process Bypass` in that window first.

Running it again is safe: it reuses the game and swaps the key. When you are done, see
"Cleaning up" below.

### By hand

1. In Studio, sign in as a developer kept for load tests (not a real customer's account).
2. Register a game named `Load test`.
3. Open its widget editor, turn clans and clan chat on, and publish it once (the scripts need a
   published config).
4. Create an API key and keep it for the next step.
5. **Tell the scripts where everything is.** In PowerShell, in the window you will run k6 from:

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

6. **Check the setup:**

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

### All three at once

`mixed.js` runs the three scenarios side by side, which is what the platform sees when a game is
live: 20% of the players are opening the widget, 50% are reading profiles, clans and friends, and
30% are in clan chat. The profile gives the total, so `steps` peaks at 500 players split
100 / 250 / 150. The limits are the three scripts' own.

```powershell
$env:LOADTEST_PROFILE = "steps"; k6 run loadtest/mixed.js
$env:LOADTEST_MIX = "10,60,30"; k6 run loadtest/mixed.js   # other proportions: widget, reads, chat
```

`LOADTEST_MIX` takes three percentages that add up to 100; a 0 leaves that scenario out.

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

`clan-chat.js` adds its own numbers, because chat is not request and answer:

- **`chat_delivery_ms`**: from one player sending a message to another player's socket receiving
  it. This is the chat lag a player feels.
- **`chat_ack_ms`**: from sending to the server confirming the message is stored.
- **`chat_send_failed`**: the share of messages refused or never confirmed within 5 seconds.
  `chat_errors` counts the refusals; the realtime service's log says which rule refused them.
- **`chat_messages_sent` / `chat_messages_received`**: in a clan of 20, each message sent is
  received about 20 times.

Each player stays connected for a minute and writes every 8 to 12 seconds, so 500 players are
about 50 messages a second stored and about 1,000 a second delivered. The test game needs clan
chat on and slow mode off (the defaults); with slow mode on, sends are refused as `slow_mode`.

Write down, per step: players, requests per second (`http_reqs`), p(95) per request kind, and the
failure rate. The first step where a limit is crossed is the capacity figure; what the server was
short of at that moment (CPU, database connections, memory) is the thing to fix. The results so far,
and the tables for the next runs, are in [RESULTS.md](RESULTS.md).

## Rules for running against the server

The server is also where the product is shown, and a load test is meant to find the point where
it stops coping.

- Run locally first. Only bring a scenario to the server once it passes locally.
- Agree a time with the team and say when you start and stop.
- Use the test game only. Never a real developer's game or key.
- Watch the server (Datadog) while it runs, and stop the test (`Ctrl+C`) if error rates climb.
- Afterwards, revoke the key and clean up the test data (below).

## Test data

Test players are numbered `lt_1`, `lt_2`, ... with the display names `Load 1`, `Load 2`, ...
The same number is the same player in every run, so runs reuse players instead of adding more.
Everything a test creates (players, clans, friendships, messages) belongs to the test game.

`social-reads.js` fills the game before it starts (`lib/seed.js`): every player is put in a clan
of up to 20 members (`LOADTEST_CLAN_SIZE`, never more than the game allows) named `LT <n> <xxxx>`,
and made friends with the three players numbered after them, so everyone has six friends. Seeding
only adds what is missing, so the first run of an environment takes a moment and later runs are
quick. One player is prepared per virtual player, up to 500 (`LOADTEST_MAX_PLAYERS`).

## Cleaning up

After a run, and always after a run on the server:

1. **Revoke the key and delete the settings file.** Load the settings file first (it holds the
   throwaway developer's sign-in), or set `LOADTEST_DEV_PASSWORD` as above for the server, then:

   ```powershell
   go run ./tools/loadtest-setup -teardown                                    # local
   go run ./tools/loadtest-setup -teardown -platform https://platform.<host> -email <address>
   ```

   Every API key of the test game is revoked, so nothing written down still works. Close the
   PowerShell window too, since the variables are still set in it.
2. **The test data stays** until it is removed: the game, its `lt_` players, their clans,
   friendships and messages. Locally, `docker compose down -v` removes everything, your other local data included. On the server,
   an admin suspends the test game in the admin panel while it is not in use, or whoever runs the
   database removes that game's rows from the social and realtime schemas (through the SSH tunnel).
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
  clan-chat.js     scenario 3
  mixed.js         the three at once
  RESULTS.md       what the runs found
tools/loadtest-setup/
  main.go          the setup tool: test game, widget config, API key, settings file, -teardown
  config.json      the widget config it publishes
```
