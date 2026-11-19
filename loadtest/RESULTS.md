# Load-test results

What the runs found, newest last. Add a section per run; keep the numbers as k6 printed them.
How to read them is in [README.md](README.md#reading-the-result).

## Summary

| Where | Scenario | Players reached | Verdict | First limit crossed |
| --- | --- | --- | --- | --- |
| local | `widget-open.js` | 500 | pass | none |
| local | `social-reads.js` | 500 | pass | none |
| local | `clan-chat.js` | 500 | pass | none |
| local | `mixed.js` | 500 (100 / 250 / 150) | pass | none |
| server | `mixed.js` | 500 (100 / 250 / 150) | no errors; 6 of 16 limits crossed by 1 to 18% | p(95) of reads at 500 players; the server's CPU was at about 95% |

No limit has been found yet: locally the platform held the full `steps` profile in every scenario,
and with all three running at once.
On the server, 500 players got no errors at all, but the server's CPU reached about 95% at 500
players and six response-time limits were crossed by a little. About 500 players is what this server
holds as it is; CPU is what runs short first.

## Local, `steps` profile, November 2026

Laptop running the whole stack in docker compose, so k6 and the services shared one machine. The
data set was small: one test game and its `lt_` players only.

| Scenario | Players | p(95) | Failed |
| --- | --- | --- | --- |
| `widget-open.js` | 500 | about 4 to 6 ms per request kind | 0% |
| `social-reads.js` | 500 | about 4 to 6 ms per request kind | 0% |
| `clan-chat.js` | 500 | delivery (`chat_delivery_ms`) 44 ms | 0% |

What this does and does not say:

- 500 players is where `steps` ends, not where the platform gives out. `stress` (up to 4,000) has
  not been run.
- One machine played both sides, so a slow step could be k6's fault as much as the platform's.
- With few rows in the database, queries stay fast; the server's numbers will be higher.

## Local, `mixed.js`, `steps` profile

Date: 2026-11-19. Mix (`LOADTEST_MIX`): 20,50,30. Same laptop and docker compose stack as above.
Ran the whole profile (11 min 45 s), peaking at 500 players: 100 opening the widget, 250 reading,
150 in clan chat. k6 prints the run as a whole, not per step, so these are totals.

**All 16 thresholds passed. 0 of 177,059 requests failed; 0 of 3,961 chat messages failed.**

| Measure | Result | Limit |
| --- | --- | --- |
| Requests | 177,059, about 251 a second on average | |
| Failed requests | 0% | under 1% |
| p(95) widget config | 5.03 ms | 300 ms |
| p(95) player token | 3.66 ms | 500 ms |
| p(95) own profile | 5.38 ms | 300 ms |
| p(95) my clan / members / clan list | 3.55 / 3.74 / 2.96 ms | 300 ms |
| p(95) friends / friend requests | 3.93 / 3.31 ms | 300 ms |
| p(95) conversations | 20.57 ms (the slowest read) | 300 ms |
| Chat sockets opened | 654, none failed; p(95) connect 7.3 ms | under 1% failed |
| p(95) chat delivery (`chat_delivery_ms`) | 46 ms | 500 ms |
| p(95) chat confirmed (`chat_ack_ms`) | 47 ms | 500 ms |
| Chat messages sent / received | 3,963 / 23,867 | |
| Slowest request of the run | 107.68 ms | |

Notes:

- Each message reached about 6 players, not about 20 as in `clan-chat.js` alone. That is expected:
  clans are made of consecutive player numbers, and here only 30% of the players are in chat at a
  time, so about 6 of a clan's 20 members are online in its room.
- `conversations` is the slowest read by about four times. It is still far inside its limit; it is
  the first thing to look at if the server's numbers come back high.
- Chat latency matches `clan-chat.js` alone (44 ms then, 46 ms now): the reads running next to it
  did not slow it down.

## Server, `mixed.js`, `steps` profile, 2026-11-19

Run from 21:04 to 21:23 (Baku time), announced to the team, against
`https://platform.130-61-158-79.sslip.io` with the test game `Load test`. k6 ran on a laptop in
Baku over home internet, so every time below includes the trip to the server and back. The smoke
test before it took about 80 ms per request: that is the starting point, not the server's time.
Mix 20,50,30; peak 500 players (100 / 250 / 150). Seeding the 500 players took about 8 minutes,
then the 10 minute 30 second ramp ran in full.

**0 of 136,100 requests failed, 0 of 3,930 chat messages failed, 0 of 650 chat sockets failed.
10 of 16 thresholds passed; 6 were crossed, each by 1 to 18%.**

| Measure | Median | p(95) | Limit | |
| --- | --- | --- | --- | --- |
| Widget config | 94.8 ms | 330.2 ms | 300 ms | crossed |
| Player token | 99.0 ms | 319.6 ms | 500 ms | passed |
| Own profile | 96.8 ms | 312.5 ms | 300 ms | crossed |
| My clan | 99.8 ms | 305.5 ms | 300 ms | crossed |
| Clan members | 101.9 ms | 328.6 ms | 300 ms | crossed |
| Clan list | 100.1 ms | 302.6 ms | 300 ms | crossed |
| Friends | 102.3 ms | 283.9 ms | 300 ms | passed |
| Friend requests | 99.7 ms | 284.8 ms | 300 ms | passed |
| Conversations | 108.3 ms | 355.4 ms | 300 ms | crossed |
| Chat delivery (`chat_delivery_ms`) | 92 ms | 249 ms | 500 ms | passed |
| Chat confirmed (`chat_ack_ms`) | 91 ms | 248.5 ms | 500 ms | passed |
| Chat socket opened (`ws_connecting`) | 286.7 ms | 682.6 ms | | |

About 120 requests a second on average over the run; the slowest request took 2.85 s.

What the server itself showed (Datadog dashboard "Clan platform – load test", same window). The
first minutes are the seeding; the ramp ran from about 21:12 to 21:23:

| Players | Host CPU | Host memory | Server-side p95 / p99 (worst service) |
| --- | --- | --- | --- |
| none (before) | about 12% | about 17% | |
| seeding | about 20% | about 17% | |
| 10 to 50 | about 25% | about 17% | |
| 200 | about 50% | about 18% | |
| 500 | **about 95%** | about 19% | about 100 / 154 ms (platform) |

No errors in any service. Memory stayed flat. The request-rate widgets of that dashboard show far
fewer requests than k6 sent (about 6 a second at most against about 120): they count something
narrower than all requests, so read their shape, not their numbers.

What this says:

- **The server holds 500 players, but only just.** Nothing failed, yet CPU went from about 50% at
  200 players to about 95% at 500, and the server's own response times rose from a few
  milliseconds to around 100 ms at the slowest 5%. Past 500, requests would queue and times would
  climb fast.
- **CPU is what runs short first**, not memory. Everything (the three services, the database,
  the collector) shares one host, so the fix is more CPU for that host, or moving the database off
  it, before any code change.
- **The crossed limits are partly the server and partly the distance.** About 80 ms of every
  request is the trip from Baku; the rest of the rise at 500 players is the server's CPU running out.
  The limits were set for the local stack.
- **`conversations` is again the slowest read**, as locally: the first endpoint to look at.
- Not tested: more than 500 players (`stress`), and load from more than one machine.

Clean-up: `-teardown` revokes the test game's key and deletes the settings file; the test data
(the `Load test` game, its `lt_` players, clans and messages) stays until an admin suspends the game
or it is removed through the SSH tunnel.
