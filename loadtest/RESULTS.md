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
| server | `mixed.js` | 500 (100 / 250 / 150) | no errors; 6 of 16 limits crossed by 1 to 18% | p(95) of reads, measured from a laptop over the internet |

No limit has been found yet: locally the platform held the full `steps` profile in every scenario,
and with all three running at once.
On the server, 500 players got no errors at all, but six response-time limits were crossed by a
little: they are measured on the laptop that runs k6, so they include the trip over the internet.

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

What the server itself showed (Datadog APM, same window): requests rose with each step and peaked
at the 500-player stage, with no errors, and the server-side response times stayed in the tens of
milliseconds; the highest percentile line peaked around 44 ms.

What this says:

- **The server held 500 players**: nothing failed, and it answered in milliseconds.
- **The crossed limits are about the distance, not the server.** A typical request took about
  100 ms from the laptop, of which about 80 ms is the round trip; the slow tail (p95 around
  300 ms, worst 2.85 s) comes from the internet connection and from one laptop playing 500
  players. The limits were set for the local stack, where requests take a few milliseconds.
- **`conversations` is again the slowest read**, as locally. It is the endpoint to look at first
  if the platform has to hold more players.
- Not tested: more than 500 players (`stress`), and load from more than one machine. For a
  capacity figure above 500, run k6 from a machine close to the server, so the times measure the
  server and not the connection.

Clean-up: `-teardown` revokes the test game's key and deletes the settings file; the test data
(the `Load test` game, its `lt_` players, clans and messages) stays until an admin suspends the game
or it is removed through the SSH tunnel.
