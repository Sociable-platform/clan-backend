# Load-test results

What the runs found, newest last. Add a section per run; keep the numbers as k6 printed them.
How to read them is in [README.md](README.md#reading-the-result).

## Summary

| Where | Scenario | Players reached | Verdict | First limit crossed |
| --- | --- | --- | --- | --- |
| local | `widget-open.js` | 500 | pass | none |
| local | `social-reads.js` | 500 | pass | none |
| local | `clan-chat.js` | 500 | pass | none |
| local | `mixed.js` | | | |
| server | `mixed.js` | | | |

No limit has been found yet: locally the platform held the full `steps` profile in every scenario.
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

Date: ____  Mix (`LOADTEST_MIX`): 20,50,30

| Step | Players (widget / reads / chat) | Requests/s | p(95) widget config | p(95) social reads | p(95) chat delivery | Failed |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 10 (2 / 5 / 3) | | | | | |
| 2 | 50 (10 / 25 / 15) | | | | | |
| 3 | 200 (40 / 100 / 60) | | | | | |
| 4 | 500 (100 / 250 / 150) | | | | | |

Thresholds: ____ of 15 passed.

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
