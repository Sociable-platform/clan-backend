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
| server | `mixed.js` | | | |

No limit has been found yet: locally the platform held the full `steps` profile in every scenario,
and with all three running at once.
The server has not been tested.

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

## Server, `mixed.js`, `steps` profile

Only at a time agreed with the team, announced before and after (see
[README.md](README.md#rules-for-running-against-the-server)).

Date and time: ____  Announced in: ____  Who watched Datadog: ____

| Step | Players | Requests/s | p(95) widget config | p(95) social reads | p(95) chat delivery | Failed | Server CPU / DB connections / memory |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 10 | | | | | | |
| 2 | 50 | | | | | | |
| 3 | 200 | | | | | | |
| 4 | 500 | | | | | | |

Capacity figure (last step with every limit passed): ____
What ran short first: ____
Clean-up done (key revoked, test data removed or game suspended): ____
