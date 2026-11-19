# Endpoints

Every HTTP endpoint and the WebSocket of the three services, and where their contracts live. Back to the [README](../README.md).

## API contracts (OpenAPI)

| Service | File |
|---|---|
| platform | `openapi/platform.yaml` |
| social | `openapi/social.yaml` |
| realtime | `openapi/realtime.yaml` (REST) and `openapi/realtime-ws.md` (WebSocket frames) |

These files are the contract the dashboard, SDK and demo game build against.
When you add or change a route, update `Routes()` in the service's `internal/handler/handler.go` **and** its OpenAPI file in the same commit.
`go test ./...` fails if a route is missing from the file, or the file lists a route that does not exist (`TestOpenAPIMatchesRoutes` in each service). [`docs/api-reference.md`](api-reference.md) lists every contract and who may call what.
To browse a file, paste it into https://editor.swagger.io or open it with the OpenAPI plugin in GoLand.

## Endpoints so far

**Suspended developers are read-only.** While an admin has the developer's account suspended, login and every `GET` still work (so the dashboard can show why),
but everything that changes data answers `403 account_suspended` before the body is read: register a game, create an API key, save allowed origins,
save a widget draft, publish and roll back. Revoking an API key stays allowed (it only takes access away, so a leaked key can still be killed).
Resuming lets writes through again at once. The check is `requireWritable` in `internal/handler`; `TestWritesRefuseASuspendedDeveloper` fails for a new write route that does not use it.
Only the developer's own switch counts here: a game suspended on its own does not stop its developers from editing. The check is on the logged-in developer, so it holds on every game they belong to, as owner or member (`TestASuspendedMemberReadsButCannotWrite`).

| Method | Path | Answer |
|---|---|---|
| GET | `/health` | 200 while the process runs |
| GET | `/ready` | 200 when the database answers, otherwise 503 `not_ready` |
| POST | `/api/v1/developers/register` | 201 with the new developer; 400 invalid email, password or name; 409 `email_taken`. With email verification on: 202, nothing created yet, a code is emailed (see [Email verification at registration](configuration.md#email-verification-at-registration)); 429 `rate_limited` |
| POST | `/api/v1/developers/register/verify` | Email verification only (else 404). 201 with the new developer for `{ email, code }`; 400 `invalid_verification_code` (wrong, expired, unknown) or `verification_attempts_exceeded` (fifth wrong code, registration cancelled); 409 `email_taken` |
| POST | `/api/v1/developers/register/resend` | Email verification only (else 404). 202 always; emails a new code when a registration is waiting for `{ email }`. 429 `rate_limited` (also within a minute of the last code) |
| POST | `/api/v1/developers/login` | 200 `{ token, expiresIn }`; 401 `invalid_credentials` |
| POST | `/api/v1/developers/token/refresh` | 200 `{ token, expiresIn }` and a new refresh cookie, for the `clan_refresh` cookie that login set; the old refresh token stops working. 401 `invalid_refresh_token` when the session is over (no cookie, expired, logged out, password reset, or a replaced token used again, which ends that session) |
| POST | `/api/v1/developers/logout` | 204 always; ends the session of the refresh cookie and clears the cookie. Other logins of the developer stay |
| POST | `/api/v1/developers/password-reset` | 202 always, for a known and an unknown email alike; emails a reset link to a known one (see "Password reset"). 400 `invalid_email`; 429 `rate_limited` |
| POST | `/api/v1/developers/password-reset/confirm` | 204 and the password is changed, with `{ token, password }`; 400 `invalid_reset_token` (unknown, used or expired) or `invalid_password`; 429 `rate_limited` |
| GET | `/api/v1/developers/me` | 200 with the developer, including `status` (`active` / `suspended`) and `statusReason` (`non_payment` / `abuse` / `other`, else `null`) so the dashboard can explain a suspension; never the admin's internal note. 401 unless `Authorization: Bearer <developer token>` |
| POST | `/api/v1/admin/login` | 200 `{ token, expiresIn }` with an **admin** token; 401 `invalid_credentials` for an unknown email, a wrong password and a disabled admin alike |
| GET | `/api/v1/admin/me` | 200 with the admin; 401 unless `Authorization: Bearer <admin token>` of an admin who is not disabled (developer and player tokens get 401) |
| GET | `/api/v1/admin/developers` | **Admin token only.** 200 `{ developers: [{ id, email, name, status, gameCount, createdAt }], nextCursor }`, newest first. Query: `q` (part of email or name), `status` (`active` / `suspended`), `limit` (1 to 100, default 25), `cursor` (the previous `nextCursor`; `null` on the last page). 400 `invalid_query` / `invalid_status` / `invalid_limit` / `invalid_cursor` |
| GET | `/api/v1/admin/developers/{developerID}` | **Admin token only.** 200 with the developer (`status`, `statusReason`, `statusChangedAt`) and all the `games` they are on, each with the same three fields and the developer's `role` on it. 404 `developer_not_found` (also for a malformed ID) |
| PUT | `/api/v1/admin/developers/{developerID}/status` | **Admin token only.** Body `{ status, reason, note }`: `status` is `active` or `suspended`; `reason` (`non_payment`, `abuse`, `other`) is **required when suspending** (400 `reason_required`; `invalid_reason`, `invalid_status`, `invalid_note` for bad values); `note` (up to 1000 characters) is internal and lives in the audit log only. 200 with the developer and their games, as `GET .../developers/{developerID}`. Suspending a developer turns off all their games without changing the game rows. The status and its audit row are written in one transaction. Setting the status the developer already has changes nothing and writes no audit row. Resuming needs no reason (the audit row then records the reason of the suspension it ends). 404 `developer_not_found` |
| GET | `/api/v1/admin/games/{gameID}/team-log` | **Admin token only.** 200 `{ entries: [{ id, action, actor: { developerId, name, email }, subject: { developerId, name, email }, role, createdAt }], nextCursor }`, newest first. Query `limit` (1 to 100, default 25) and `cursor`. `developerId` is null for an invitation to an address with no account; `role` is the new role of a `role_changed`, else null. 404 `game_not_found`; 400 `invalid_limit` / `invalid_cursor` |
| GET | `/api/v1/admin/games/{gameID}` | **Admin token only.** 200 with the game (`status`, `statusReason`, `statusChangedAt`) and its `members`: `[{ developerId, name, email, role, status, joinedAt }]`, owners first, each with the status of their account, so the panel shows who is on a game and why it is off. 404 `game_not_found` (also for a malformed ID) |
| PUT | `/api/v1/admin/games/{gameID}/status` | **Admin token only.** Same body and rules for one game; 200 with the game (`status`, `statusReason`, `statusChangedAt`). 404 `game_not_found` |
| GET | `/api/v1/admin/audit-log` | **Admin token only.** 200 `{ entries: [...], nextCursor }`, newest first; `targetId` limits it to one developer or game; `limit` and `cursor` as above. `note` is internal and is never shown to developers. 400 `invalid_target_id` / `invalid_limit` / `invalid_cursor` |
| GET | `/api/v1/games` | 200 `{ games: [...] }` with only the games the logged-in developer belongs to, newest first (each with its `allowedOrigins`); 401 without a developer token |
| POST | `/api/v1/games` | 201 with the registered game (registers an existing game, never creates one); 400 `invalid_name` / `invalid_description`; 401 |
| GET | `/api/v1/games/{gameID}/members` | 200 `{ members: [{ developerId, name, email, role, joinedAt }] }`: everybody on the game, owners first, then members by when they joined. Any member may look; 404 `game_not_found` unless you belong to the game |
| DELETE | `/api/v1/games/{gameID}/members/{developerID}` | **Owners only.** 204; the developer loses access at once (their next request for the game is 404). Removing an owner is allowed while another is left. 404 `member_not_found`; 409 `last_owner`; 403 `owner_required` for a member |
| PUT | `/api/v1/games/{gameID}/members/{developerID}/role` | **Owners only.** Body `{ role: "owner" \| "member" }`: promote or demote. 200 with the member. A role they have already is not a change. 400 `invalid_role`; 404 `member_not_found`; 409 `last_owner` when demoting the last owner |
| POST | `/api/v1/games/{gameID}/transfer-ownership` | **Owners only.** Body `{ developerId }`: that member becomes an owner and the caller a member, in one transaction. 200 with the game (`role: member`). 404 `member_not_found`; 409 `already_owner` |
| POST | `/api/v1/games/{gameID}/leave` | Any member. No body. 204; the game is a 404 for them afterwards. An owner can leave only while another owner is left: 409 `last_owner` |
| POST | `/api/v1/games/{gameID}/invites` | Body `{ email }`. **Owners only.** Invites an address to the game: 201 `{ id, email, invitedByName, createdAt, expiresAt }`, and an email goes to the address. Inviting again renews the invite (one pending per game and address). 400 `invalid_email`; 409 `already_member`; 429 `rate_limited`; 403 `owner_required` for a member; 404 `game_not_found` unless you belong to the game |
| GET | `/api/v1/games/{gameID}/invites` | **Owners only.** 200 `{ invites: [...] }`: the pending, unexpired invites, newest first |
| DELETE | `/api/v1/games/{gameID}/invites/{inviteID}` | **Owners only.** 204; the invite can no longer be accepted. 404 `invite_not_found` |
| GET | `/api/v1/invites` | The invites sent to the email address of the logged-in developer: 200 `{ invites: [{ id, gameId, gameName, invitedByName, createdAt, expiresAt }] }`, newest first, not expired. Open to a suspended developer |
| POST | `/api/v1/invites/{inviteID}/accept` | No body. Makes the developer a `member` of the game and deletes the invite, in one statement: 200 with the game (`role: member`). 404 `invite_not_found` for an invite that does not exist **and** for one sent to another address; 410 `invite_expired`; 403 `account_suspended` |
| POST | `/api/v1/invites/{inviteID}/decline` | No body. Deletes the invite and does nothing else: 204. 404 `invite_not_found`; 403 `account_suspended` |
| PUT | `/api/v1/games/{gameID}/allowed-origins` | Body `{ allowedOrigins: [...] }` replaces the list of websites that may open a WebSocket for the game (200 with the game). Each origin is `scheme://host[:port]` as the browser sends it, `http` or `https`, no path or trailing slash, no wildcard; hosts are lower-cased, repeats dropped, at most 20. `[]` clears it. 400 `invalid_origins` (nothing is saved); **owners only**: 403 `owner_required` for a member, 404 `game_not_found` unless you belong to the game |
| POST | `/api/v1/games/{gameID}/api-keys` | 201 with the new key; **the full `key` is in this response only** (the database stores just a hash). **Owners only**: 403 `owner_required` for a member, 404 `game_not_found` unless you belong to the game |
| GET | `/api/v1/games/{gameID}/api-keys` | 200 `{ apiKeys: [...] }`: prefix, created and revoked time, never the key. **Owners only**: 403 `owner_required` for a member, 404 unless you belong to the game |
| DELETE | `/api/v1/api-keys/{keyID}` | 204; the key stops working at once and stays listed as revoked. **Owners only**: 403 `owner_required` for a member of the key's game; 404 `api_key_not_found` unless it is a key of a game you belong to |
| GET | `/api/v1/games/{gameID}/widget/draft` | 200 `{ config, updatedAt }`: the widget config being edited, or the default config with `updatedAt: null` when nothing was saved yet. Players never see drafts. 404 `game_not_found` unless you belong to the game |
| PUT | `/api/v1/games/{gameID}/widget/draft` | 200 with the saved draft. Body `{ "config": { ... } }`: a JSON object of at most 64 KB that replaces the whole draft. 400 `invalid_widget_config` / `invalid_json`; 404 unless you belong to the game |
| POST | `/api/v1/games/{gameID}/widget/publish` | 201 with the new version `{ version, publishedAt, publishedBy, config }`. No body: it publishes the saved draft (the draft stays as it is). 409 `widget_draft_missing` if nothing was saved; 400 `invalid_widget_config`; 404 unless you belong to the game |
| GET | `/api/v1/games/{gameID}/widget/versions` | 200 `{ versions: [...] }`, newest first, without configs; an empty list if never published. 404 unless you belong to the game |
| GET | `/api/v1/games/{gameID}/widget/versions/{version}` | 200 with that version and its `config`. 404 `widget_version_not_found` (also for a number that is not a positive whole number) |
| POST | `/api/v1/games/{gameID}/widget/versions/{version}/rollback` | 201 with a **new** version that has the old version's config; nothing is deleted and the draft is untouched. 404 `widget_version_not_found` |
| GET | `/api/v1/public/games/{gameID}/widget-config` | **No login, open to every website.** 200 `{ version, config }`: the latest **published** version (never a draft), with a strong `ETag` (`"v3"`) and `Cache-Control: public, max-age=60`. Send `If-None-Match` to get 304. 404 `widget_not_published` (also for an unknown game ID). **403 `game_suspended`** (with `Cache-Control: no-store`) when the game or its developer is suspended, whichever it is, so players never see a billing message; the SDK then removes the panel. Not rate-limited in the service: put a CDN or proxy limit in front of `/api/v1/public/` |
| GET | `/internal/v1/games/{gameID}/config` | **For other services, not browsers.** Needs `X-Internal-Token: <PLATFORM_INTERNAL_TOKEN>` (401 `invalid_internal_token`; 404 `not_found` while the secret is not set). 200 `{ gameId, version, status, config, allowedOrigins }`: the newest **published** config, the game's allowed origins and its **effective** `status` (`suspended` when the game or its developer is, else `active`; a suspended game still gets its config), never cached. A game never published has `version: 0` and `config: null` but still its origins. 404 `game_not_found` for an unknown game |
| any | anything else | 404 `not_found` in the standard error format |
| any | from a website not in the allowed origins | 403 `origin_not_allowed` |

**Who belongs to a game (TEAM-305).** A game's developers are rows of `game_members (game_id, developer_id, role)`, with role `owner` or `member` (migration `000015`; the old `games.developer_id` column was dropped by `000018`, see "Team history" below). Registering a game makes the developer its owner in the same statement, and the migration made every existing game's developer its owner, so nothing changed for anybody. Every developer route under `/api/v1/games/{gameID}` goes through `requireGameMember`, which looks the role up on every request (it is per game and is never in the login token) and answers 404 `game_not_found` to a developer who is not in the game, the same as for a game that does not exist or an ID that is not a UUID, so the answer never tells whether somebody else's game exists. The service methods check again. `TestEveryGameRouteChecksMembership` walks `Routes()` and fails for a game route that skips the gate. Members are added by invitation (TEAM-307, below).

**Roles (TEAM-306).** Every game in `GET /api/v1/games` (and in the register and allowed-origins responses) carries `role`, the asking developer's role on it. An **owner** may do everything. A **member** works on the game: reads and edits the widget draft, publishes, rolls back, reads versions and sees the team (`GET .../members`). What *manages* the game is owner-only: API keys (create, list, revoke) and allowed origins. A member who tries gets **403 `owner_required`** (not 404: they know the game); a developer who is not in the game still gets 404 `game_not_found`. Owner-only routes add `requireGameOwner` after `requireGameMember`, the service checks again (`OwnerGame`), and the two SQL statements that change keys and origins check the role themselves, so a role that changes mid-request cannot let a member through. `gameRouteRole` in `handler/game_member_test.go` classifies every game route as owner or member, and `TestEveryGameRouteSaysWhoMayUseIt` fails for a route nobody classified. Suspension is unchanged: a suspended developer's writes are refused (`account_suspended`) whatever their role. `PublishedBy` on a version records the member who published.

**Invitations (TEAM-307).** An owner invites a developer to a game by email address. The invite is a row of `game_invites (game_id, email, invited_by, expires_at)` (migration `000016`), one pending per game and address, valid for **7 days**; expired rows are deleted when the next invite is created and never listed or accepted. It holds **no token and no link secret**: the email says "log in" (the address has an account) or "register" (it has none) and points at the dashboard, and the invite waits for the **address**. Whoever is logged in with an account whose email is that address sees it in `GET /api/v1/invites`; a person with no account registers first (which confirms the address when `PLATFORM_EMAIL_VERIFICATION` is on, so the address is verified before they can accept) and finds it waiting. The match is on the account's email, so holding the email or the invite's ID is not enough: someone else's invite is 404 `invite_not_found`, the same as one that does not exist. Accepting inserts the `member` row and deletes the invite in a single statement, so it is both or neither; declining, cancelling or expiring creates nothing; accepting when already on the game just uses the invite up and keeps the role. The invitation email goes through the same mailer and SMTP settings as the registration and password reset emails (no new variables; without `PLATFORM_SMTP_HOST` it is written to the log). Sending is rate limited, counted in `game_invite_log` (kept one day) because invite rows are deleted when answered: **20 per game per hour, 3 per game and address per day, 5 per address per day from all games**; over a limit is 429 `rate_limited` and nothing is sent. The answer to an invite is the same whether or not the address has an account, so inviting does not reveal who has one; `409 already_member` is only for somebody who is already on the team the inviter can see. Suspension: a suspended developer can read their invites but not answer one.

**Team management (TEAM-308).** An owner removes a member, changes a role, or hands the game over; any member can leave. Access follows the `game_members` row, which every request looks up (`requireGameMember`), so a removed developer is refused on their very next request even though their login token is still valid. **A game keeps at least one owner**: every change that can take an owner away (`RemoveGameMember`, `SetGameMemberRole`, `TransferGameOwnership` in the repository) first locks the game's row (`for no key update`) and then counts the owners, so two owners leaving or demoting each other at the same moment cannot both pass: one succeeds and the other gets 409 `last_owner` (`TestTheLastOwnerSurvivesConcurrentChanges` runs these races on Postgres, and fails when the lock is removed). A transfer swaps the two roles in one statement, so the number of owners never changes. Owners are equals: an owner may remove or demote another owner as long as one is left. **Suspension with a team:** a game is served while its own switch is on and **at least one owner's account is active** (`GameAccessOf`); a suspended member never stops a game, a suspended owner stops it only when no other owner is active, and a game with one owner behaves exactly as before teams. Because a team change can flip that (the last active owner leaves, or an active member is made an owner), `changeTeam` compares the game's access before and after and writes `game.suspended` / `game.resumed` to the outbox in the same transaction. Admin: `GET /api/v1/admin/games/{gameID}` shows the members with their account status, the developer detail shows the role on each game, and `gameCount` counts the games a developer is on. `games.developer_id` (who registered the game) no longer decides anything: it was dropped in TEAM-310.

**Team history and the dropped column (TEAM-310).** Every change to a game's team is written to `game_team_log` (migration `000017`) **in the transaction of the change itself** (the service calls `logTeam` inside the same `InTx`; `TestTeamLogIsPartOfTheChange` shows a failed transaction keeps neither): `invite_sent`, `invite_cancelled`, `invite_accepted`, `invite_declined`, `member_removed`, `member_left` (an owner removing themselves is recorded as this), `role_changed` (with the new role) and `ownership_transferred`. A row says who did it (`actor_id`) and to whom (`subject_id`, or `subject_email` for an invitation: it is for an address and the person may have no account yet). Refused attempts, a role somebody already has and an expired invitation nobody joined with are not recorded: nothing changed. The history belongs to the game (`on delete cascade`) and nothing but the admin panel reads it: `GET /api/v1/admin/games/{gameID}/team-log?limit=&cursor=` (newest first, paged like the audit log). **`games.developer_id` is gone** (migration `000018`): nothing reads it, `CreateGame` makes the registering developer an owner through `game_members` alone, and `TestGamesHasNoDeveloperIDColumn` fails if it comes back. **Deploy `000018` one release after `000015`, not with it:** until it runs, the column is what the release before teams reads, so rolling the application back stays safe; `000017` can ship with the teams. The down migration of `000018` puts the column back (not null, with its index) from the owners, the one who joined first when a game has several, so the old application can start again (the other owners lose access in that application, which knows one developer per game); it was checked on the 105 games of a development database.

**Postman collection for the team (TEAM-306, TEAM-307, TEAM-308).** `postman/platform-game-team.postman_collection.json` (94 requests, 165 checks) makes its own developers and game and runs the whole flow in order: an owner invites an existing account, the invited developer lists, accepts and declines, a member is refused every owner-only action (403 `owner_required`) while still doing the member work, a stranger gets 404 everywhere, cancelling, the rate limit, and (TEAM-308) removing a member (their next request is a 404), leaving, promoting and demoting, handing the game over, and `last_owner`, and (TEAM-310) the admin reading the team history, every kind of change, with paging. The last folder needs an admin (`go run ./tools/createadmin`, then `adminEmail` and `adminPassword`). It needs Platform on `{{baseUrl}}` (default `http://localhost:8081`) with `PLATFORM_EMAIL_VERIFICATION` off, can be run again and again, and needs no database access; the 7-day expiry is covered by the Go tests.

### Social service (port 8082)

Every `/api/v1/` route needs `Authorization: Bearer <player token>`. The game and the player
always come from the token, never from the URL or the body.

**Suspended games are refused.** While an admin has the game, or its developer, switched off, every `/api/v1/` route answers
`403 game_suspended` (checked after the token, so an invalid token still gets 401; `/health` and `/ready` are not affected).
Social learns it from the call it already makes for the clan rules: the platform's public widget config answers 403
`game_suspended` for a suspended game. The answer is kept for `SOCIAL_CONFIG_CACHE_TTL`, so a suspension and a resume both
take effect within that time. Nothing is changed or deleted: friends, clans, blocks and players stay as they were. If
the platform cannot be reached, Social keeps the last status it saw, and a game it has never seen is served (fail open).

**Deleted players are refused.** After `DELETE /api/v1/players/me`, every token of that player issued before the call (or
in the same second, as `iat` has whole seconds) answers `401 player_deleted` on every `/api/v1/` route, although it has not
expired. A token for the same `externalPlayerId` issued later starts a new, empty player (TEAM-249). If the database cannot
be asked, the request goes on (fail open) and fails on its own.

| Method | Path | Answer |
|---|---|---|
| GET | `/api/v1/players/me` | 200 with the calling player; the record is created on the first call |
| DELETE | `/api/v1/players/me` | The game deleted the player's account: the game's server gets a token for the player as usual and calls this. 204, no body. In one transaction the player leaves their clan (as with `/leave`), and their friendships, pending friend requests and blocks are removed; they are renamed `Deleted player` and never found by the player search again. Their older tokens then get 401 `player_deleted` (see above). A player Social never saw: 204. Publishes `clan.member_left` (or `clan.disbanded`), `friend.removed`, `friend.request_cancelled` / `friend.request_declined`, `player.unblocked`, then `player.renamed` with the new name: the existing types, so Realtime needs no change |
| GET | `/api/v1/players?q=&limit=` | 200 `{ players: [...] }`: players of the caller's game whose name contains `q` (ignoring upper/lower case; `%` and `_` are normal characters), sorted by name, never the caller and never a deleted player. Each has `id`, `displayName`, `relation` (`none` / `friend` / `request_sent` / `request_received` / `blocked`, a player the caller blocked) and, for the two pending ones, `requestId`. Players who blocked the caller are left out. `q` is required: 400 `invalid_query` (missing, empty or over 50 characters); `limit` 1 to 100, default 100 (400 `invalid_limit`) |
| GET | `/api/v1/players/me/clan` | 200 `{ clan, role }` with the caller's clan and role (`owner` / `member`); 404 `not_in_clan` |
| GET | `/api/v1/players/me/blocks` | 200 `{ blocks: [...] }` with `id`, `displayName`, `since` (when the caller blocked them), newest first |
| POST | `/api/v1/players/{playerID}/block` | no body. 204: the caller blocked that player (see "Blocks" below); blocking again changes nothing (204, no event). 400 `cannot_block_self`; 404 `player_not_found` (also for another game's player). Publishes `player.blocked` |
| DELETE | `/api/v1/players/{playerID}/block` | 204: the block is removed; a friendship or request it ended does not come back. Not blocked: 204, no event. 400 `cannot_block_self`; 404 `player_not_found`. Publishes `player.unblocked` |
| GET | `/api/v1/clans?q=&limit=&sort=&hasSpace=` | 200 `{ clans: [...] }` of the caller's game, by default sorted by name, at most 100. `q` keeps clans whose name contains it (ignoring upper/lower case, over 50 characters gives 400 `invalid_query`) and searches **all** clans, full ones too. `limit` 1 to 100 (400 `invalid_limit`); `sort` `name` or `members`, biggest first (400 `invalid_sort`); `hasSpace=true` leaves out full clans (400 `invalid_has_space`). Featured clans = `?hasSpace=true&sort=members&limit=3` |
| POST | `/api/v1/clans` | body `{ "name": "Wolves", "joinPolicy": "closed" }` (`joinPolicy` is `open` when left out). 201 with the new clan, the caller becomes its owner. 400 `invalid_json` / `invalid_clan_name` (3 to 20 characters) / `invalid_join_policy`; 409 `clan_name_taken` / `already_in_clan`. Clans have no tags (TEAM-277): a `tag` in the body, which older SDKs send, is ignored, and no clan answer has one |
| PATCH | `/api/v1/clans/{clanID}` | body `{ "name": "Grey Wolves" }` or `{ "joinPolicy": "closed" }` (one of the two, else 400 `invalid_clan_update`; `joinPolicy` opens or closes the clan, 400 `invalid_join_policy`, no event). 200 with the clan. Only the **owner**; the name is checked like on create. The same name changes nothing (200, no event); a change of upper/lower case only is a rename. 400 `invalid_json` / `invalid_clan_name`; 403 `not_clan_owner`; 404 `clan_not_found`; 409 `clan_name_taken`. Publishes `clan.renamed` |
| POST | `/api/v1/clans/{clanID}/join` | no body. 200 with the clan and its new `memberCount`, the caller joins as a member. **Only an open clan**: a closed one answers 403 `clan_closed` (ask with `POST .../requests` instead). Joining ends the caller's pending requests. 404 `clan_not_found` (also for another game's clan); 409 `already_in_clan` / `clan_full`: the clan has as many members as the game allows (`clan.maxMembers` from the published config, 10 before the first publish). Lowering the limit removes nobody; a clan over it just takes no one new |
| POST | `/api/v1/clans/{clanID}/leave` | no body. 204: the caller left the clan and can join another one. A normal member is just removed. If the **owner** leaves and others remain, the member who joined first becomes the owner; if the owner is the only member, the clan is deleted. 404 `clan_not_found` (also for another game's clan); 409 `not_in_clan` (the caller is not a member of this clan) |
| GET | `/api/v1/clans/{clanID}/requests` | The pending requests to join a closed clan, oldest first: `{ "requests": [{ "id", "playerId", "displayName", "createdAt" }] }`. Only a **leader**: the owner, or a role with `canHandleRequests` (a role without the field: `canKick`). 403 `not_allowed` for anybody else; 404 `clan_not_found` |
| POST | `/api/v1/clans/{clanID}/requests` | no body. The caller asks to join a **closed** clan. 201 with the request. 404 `clan_not_found`; 409 `clan_open` (join directly) / `already_in_clan` / `already_requested` / `clan_full` / `clan_requests_full` (50 pending per clan) / `too_many_requests` (5 pending per player). Publishes `clan.join_requested` |
| DELETE | `/api/v1/clans/{clanID}/requests/me` | The caller takes their request back. 204. 404 `clan_not_found` / `request_not_found`. No event |
| POST | `/api/v1/clans/{clanID}/requests/{requestID}/accept` | A leader lets the player in: 204. They become a member with the game's first role, in one transaction with the clan row locked, and all their pending requests end. 403 `not_allowed`; 404 `clan_not_found` / `request_not_found`; 409 `clan_full` (the request stays) / `already_in_clan`. Publishes `clan.member_joined` |
| POST | `/api/v1/clans/{clanID}/requests/{requestID}/decline` | A leader turns the player down: 204, and they may ask again. 403 `not_allowed`; 404 `clan_not_found` / `request_not_found`. Publishes `clan.join_request_declined` |
| GET | `/api/v1/clans/{clanID}/members` | 200 `{ members: [...] }` with `playerId`, `displayName`, `role`, `joinedAt`; owner first, then by join time. `role` is a role id of the game's `clan.roles` (see "Clan roles" below). 404 `clan_not_found` (also for another game's clan) |
| DELETE | `/api/v1/clans/{clanID}/members/{playerID}` | no body. 204: the caller removed that member (`playerId` from the member list): their role needs `canKick` and the member must have a lower role (the owner may remove anyone). The player can join a clan again at once. 403 `not_allowed`; 400 `cannot_kick_self` (leave instead); 404 `clan_not_found` / `member_not_found`. Publishes `clan.member_left` with `reason` `kicked` |
| POST | `/api/v1/clans/{clanID}/members/{playerID}/promote` | no body. 200 `{ playerId, displayName, role }`: the member is one role up. The caller's role needs `canPromote`, the member must be lower, and the new role stays below the caller's own (nobody becomes owner). 403 `not_allowed`; 404 `clan_not_found` / `member_not_found`. Publishes `clan.member_role_changed` |
| POST | `/api/v1/clans/{clanID}/members/{playerID}/demote` | no body. 200 `{ playerId, displayName, role }`: one role down, same rules. 409 `already_lowest_role`; 403 `not_allowed`; 404 `clan_not_found` / `member_not_found`. Publishes `clan.member_role_changed` |
| GET | `/api/v1/friends` | 200 `{ friends: [...] }` with `id`, `displayName`, `since` and `clan`, sorted by name. `clan` is the friend's clan in the same game now (`{ id, name }`), or `null` when they are in none (also after leaving, a kick or a disband); it comes from the same query, so there is no request per friend. Accept and a request sent back return the friend without `clan` |
| GET | `/api/v1/friends/requests` | 200 `{ incoming: [...], outgoing: [...] }`, newest first; each has `id`, `player` (the other player: `id`, `displayName`) and `createdAt` |
| POST | `/api/v1/friends/requests` | body `{ "playerId": "<social player ID>" }`. 201 with the new request. If that player had already asked the caller, it is accepted instead: 200 with the new friend. 400 `invalid_json` / `invalid_player_id` / `cannot_friend_yourself`; 404 `player_not_found` (also for another game's player, or a player who blocked the caller); 409 `already_friends` / `friend_request_exists` / `player_blocked` (the caller blocked that player). Publishes `friend.request_sent` or `friend.accepted` |
| POST | `/api/v1/friends/requests/{requestID}/decline` | no body. 204: the request sent to the caller is deleted. 403 `not_request_receiver` (the caller sent it; cancel instead); 404 `friend_request_not_found`. Publishes `friend.request_declined` |
| DELETE | `/api/v1/friends/requests/{requestID}` | 204: the caller's own request is cancelled. 403 `not_request_sender` (the caller received it; decline instead); 404 `friend_request_not_found`. Publishes `friend.request_cancelled` |
| DELETE | `/api/v1/friends/{playerID}` | 204: the friendship ends for both players; either may send a new request afterwards. 404 `not_friends` (also for another game's player). Publishes `friend.removed` |
| POST | `/api/v1/friends/requests/{requestID}/accept` | no body. 200 with the new friend. 403 `not_request_receiver` (the caller sent it); 404 `friend_request_not_found`. Publishes `friend.accepted` |

**Clan roles.** Each game sets its clan roles in Studio (`clan.roles` in its published config, lowest power first,
2 to 12). Social reads them with the clan size (`SOCIAL_PLATFORM_URL`, cached `SOCIAL_CONFIG_CACHE_TTL`) and decides every
kick, promote and demote by them, inside the clan's row lock (package `internal/roles`):

- A new member gets the first role. The owner (`clans.owner_id`) always has the last role; their row stores `owner`.
- A member acts only on someone with a lower role, never on an equal, a higher one or themselves. Kick needs `canKick`;
  promote and demote need `canPromote`. A promote stays below the caller's own role, so nobody becomes owner.
- Answering requests to join a closed clan needs `canHandleRequests`; any role may have it, the first one included, and
  the owner always does. A role without the field (configs from before it) has it when it has `canKick`.
- A role id the developer removed or renamed reads as the first role: nobody is removed. Before a game publishes
  roles there are two, `member` and `owner`, and only the owner may kick (as before roles).
- `clan_members.role` stores the role id; the database only checks its form (migration `000005`).

**Blocks.** A player can block another player of the same game (table `blocks`, migration `000006`). Blocking is
one-way and the blocked player is not told:

- In one transaction, under the same pair lock as every friend change, the block ends the two players' friendship and
  their pending friend request in either direction. Social publishes `player.blocked` and then `friend.removed`,
  `friend.request_cancelled` (the blocker had asked) or `friend.request_declined` (the blocked player had asked).
- The blocked player's friend requests to the blocker answer 404 `player_not_found`, and the blocker is left out of
  their search, so the block stays hidden. The blocker's own request answers 409 `player_blocked` until they unblock.
- Realtime keeps its own copy of the blocks from the two events and refuses direct messages between the pair.
- Unblocking brings nothing back: the two have to become friends again. Their DM stays read-only for both until then
  (as after any ended friendship, TEAM-283), and opens again with its history when they do.

**Internal endpoints** are for other services, not for the SDK. They need `X-Internal-Token: <SOCIAL_INTERNAL_TOKEN>`
(401 `invalid_internal_token` otherwise; a player token is not accepted). They are the only routes that
see more than one game.

| Method | Path | Answer |
|---|---|---|
| GET | `/internal/v1/friendships?after=&limit=` | 200 `{ friendships: [...], nextCursor }`: every friendship of every game, page by page, in a fixed order. Each has `gameId`, `playerA` and `playerB` (`id`, `displayName`; `playerA` has the smaller ID) and `since`. `limit` 1 to 500, default 200 (400 `invalid_limit`). Pass `nextCursor` as `after` for the next page; it is missing on the last page; `after` must come from a previous page (400 `invalid_cursor`). The realtime backfill (TEAM-151) reads it |

### Realtime service (port 8083)

**Realtime backfill.** Realtime learns about friendships from events, so it knows nothing about friendships made
before it ran, or whose event was lost. With `REALTIME_BACKFILL=true` (and `REALTIME_INTERNAL_TOKEN` equal to social's
`SOCIAL_INTERNAL_TOKEN`) it reads every friendship from social's `GET /internal/v1/friendships`, page by page, and stores
the DM conversation of each pair. It runs once, after the database is connected and **before** the event consumer and
the HTTP server start, so events then arrive on top of a complete state. Details:
- It only adds. A conversation whose friendship Social no longer lists (a lost `friend.removed`) is left as it is.
- It is safe to leave on and to run again: stored conversations, members and messages are reused, names are updated.
- No frames are sent; nobody is told anything.
- A page that social cannot answer is tried again (5 tries, 1 s doubling to 8 s). When it keeps failing realtime logs
  `backfill failed, starting anyway` and runs without it; restart with the flag on to try again. A wrong token (401 or 404
  from social) is reported the same way.
- A friendship with an invalid ID or no name is skipped and counted (`skipped` in the `backfill done` log).
- Blocks and clans are **not** backfilled yet: social has no internal endpoint that lists blocks, and realtime has no clan chat.
- State rebuild at startup is not needed: realtime keeps friends, memberships and blocks in its own database and reads
  them from there, so a restart already keeps chat working for everything it had stored.

The WebSocket endpoint, the chat, the conversation list and the message history are built. The contract is in
`openapi/realtime.yaml` (REST) and `openapi/realtime-ws.md` (WebSocket). The REST endpoints need
`Authorization: Bearer <player token>`; the token is checked with platform's public key and realtime then asks the social
service (`REALTIME_SOCIAL_URL`, with the player's own token) for the player's social ID, so they answer 503 `social_unavailable`
while social is down.

| Method | Path | Answer |
|---|---|---|
| GET | `/health` | 200 while the process runs |
| GET | `/ready` | 200 when the database answers, otherwise 503 `not_ready` |
| GET | `/api/v1/ws?token=<player token>` | WebSocket upgrade (101). Checked **before** the upgrade: 401 `missing_token` / `invalid_token` / `token_expired` / `wrong_token_type` for the token, 403 `origin_not_allowed` when the page's `Origin` is not in `REALTIME_CORS_ORIGINS` (requests without `Origin`, like Postman's, are allowed), 503 `social_unavailable` when the social service cannot be reached (realtime asks social for the player's own ID with `GET /api/v1/players/me` and the player's token, so **social must be running**), 503 `shutting_down`. After the upgrade the server first sends a `hello` frame with the player's social ID; a `ping` frame is answered with `pong`; a frame over 8 KB closes the connection (1009); a bad frame gets an `error` frame with `bad_frame` and the connection stays; the connection is closed with 4001 when the token expires and with 1001 when the service stops; a client that does not answer the server's pings (every 25 s) for 60 s is dropped; a client that does not read its frames (64 waiting) is closed with 4002 without holding anyone else up; a **suspended game** (an admin switched off the game or its developer) gets 4403 `game_suspended` instead of `hello`, and every open connection of that game is closed with it when the `game.suspended` event arrives, within seconds (other games are not touched; if platform cannot be reached the last status seen is kept, and a game never seen is served). Public URL through Traefik: `/realtime/v1/ws` |
| GET | `/api/v1/conversations` | 200 `{ "conversations": [...] }`: every conversation of the caller in the token's game, most recently active first, each with `members` (`online` = has an open WebSocket, or closed the last one less than 10 s ago), `lastMessage` (or `null`) and `unread` (text messages of other players after the caller's read position, at most 99; member lines never count). Every message has a `kind`: `text`, or in a clan room a member line `member_joined` / `member_left` / `member_kicked` with the player as sender and an empty body (TEAM-291); a `member_kicked` line also has `kickedBy` (`{ id, name }`), who removed the player, and no other message has that field. 401 `missing_token` / `invalid_token` / `token_expired` / `wrong_token_type`; 403 `origin_not_allowed`; 503 `social_unavailable` |
| GET | `/api/v1/conversations/{conversationId}/messages?before=<messageId>&limit=50` | 200 `{ "messages": [...], "hasMore": true }`: **newest first**; `before` returns only older messages, `limit` is 1 to 100 (default 50). 400 `invalid_limit` / `invalid_before`; 403 `not_member` (not in it, or an unknown id or another game's: one answer for all); plus the errors above |
| any | anything else | 404 `not_found` in the standard error format |

**Try the WebSocket (Postman or any client).** Social and realtime must both be running (`docker compose up`). Make a player token
(it must be signed with the private key whose public key the services have), then connect through the gateway and send a ping:

```bash
go run ./tools/devtoken -player u_42 -name Ali        # prints a token
# Postman: New > WebSocket, URL  ws://localhost/realtime/v1/ws?token=<token>   (or ws://localhost:8083/api/v1/ws?token=<token>)
# On connect you get: {"event_id":"...","type":"hello","occurred_at":"...","game_id":"<game>","data":{"playerId":"<social uuid>","configVersion":<published config version or 0>,"onlineFriends":[]}}
# Then send:           {"type":"ping","id":"p-1","data":{}}
# You get:             {"event_id":"...","type":"pong","occurred_at":"...","game_id":"<game>","data":{"replyTo":"p-1"}}
# Every frame the server sends has the envelope of a queue event: event_id, type, occurred_at, game_id, data.
# Frames you send stay {"type","id","data"}.
```

Two players: make a second token with `go run ./tools/devtoken -player u_43 -name Rashad`, open a second Postman WebSocket tab
with it. Each tab gets its own `hello` with a different `playerId`; disconnecting one does not affect the other.

A missing or wrong token makes Connect fail with HTTP 401 and a JSON error body. The token is in the URL, so the service
and Traefik never log the query string. In `hello`, `configVersion` is the version of the game's published config (`0` when there is none, or platform did not answer in a second) and `onlineFriends` lists the friends (people the player has a DM with) who are online now (a friend who closed their last tab less than 10 s ago still counts). When the game publishes a config, realtime fetches it from platform and sends `config.updated { version }` to every open connection of that game.

**Chat between two friends (first full slice).** Two players only share a conversation after a `friend.accepted` event (Social does not
publish it yet; publish it by hand, see [RabbitMQ events](rabbitmq.md#rabbitmq-events) and its "Check by hand" bullet, with the `playerId`s from the two `hello` frames and
the `gid` of the tokens as `game_id`). Both tabs then get `conversation.added`; copy `data.conversation.id`. In either tab send:

```
{"type":"message.send","id":"c-1","data":{"conversationId":"<conversation id>","body":"  hello  "}}
```

The sender gets `message.ack` (`replyTo: "c-1"`, `messageId`, `createdAt`) and then, like the friend and every other open tab of either player,
`message.new` with the trimmed text. Check the rows with
`docker compose exec postgres psql -U postgres -d clan -c "select id, sender_name, body, created_at from realtime.messages order by id"`.
Errors come back as `error` frames with `replyTo` and nothing is stored or sent: `not_member` (not in the conversation, or an unknown id),
`rate_limited`, `feature_disabled`, `blocked`, `empty`, `too_long`, `slow_mode` (with `retryAfterMs`), `bad_frame` (missing or wrong-type field),
`internal` (for example the database is down).
**Message rules.** `message.send` is checked in this order, and the first failure answers (full table in `openapi/realtime-ws.md`): rate limit of the connection (5 in 5 s; three refused in a row close it with `1008`), not empty, member, the game's chat switches (`chat.enabled`, `directMessages`, `clanChat`), not blocked (DMs), `chat.maxMessageLength` in characters, slow mode (`chat.slowModeSeconds` per player across all tabs), then the profanity filter (`chat.profanityFilter`, whole words, replaced with `***`; the list is `services/realtime/internal/profanity/words.txt`). The game's rules come from its published config (kept in memory, updated on `config.published`); a game without a published config has the permissive defaults. Blocks come from the `player.blocked` and `player.unblocked` events into Realtime's own `blocks` table (Social publishes them on block and unblock).
Presence: a player is online while one of their WebSockets is open. When the last one closes, realtime waits 10 s; if the player did not connect again, the friends who are connected get `presence.update { playerId, status: "offline" }`, so a page refresh never shows as offline. A player's first connection, when they were offline, sends their connected friends `presence.update { playerId, status: "online" }` (TEAM-274); another tab, or coming back within the 10 s (a refresh), sends nothing. A new friendship between two online players gives each a `presence.update` `online` for the other. The 10 s timers are in memory of the one process: if realtime restarts during them, nobody is told that player went offline.
Slow mode and the rate limit live in the memory of the one Realtime process (a restart forgets them).

**Clan chat.** Realtime makes each clan's chat room from Social's clan events, so creating or joining a clan with a player token
(`POST http://localhost:8082/api/v1/clans` with `{"name":"Wolves"}`, `POST http://localhost:8082/api/v1/clans/<clan id>/join`) is all it
takes: that player's tab gets `conversation.added` with `kind: "clan"` and the `clanId`, and the members already in the clan get a
`clan_joined` notification; every member also gets a `member_joined` line as `message.new` (`kind: "member_joined"`, the
player as sender, empty body). Send `message.send` with the room's conversation id exactly as in a DM; every member gets `message.new`.
Blocks do not apply in a clan room, and `chat.clanChat: false` in the published config answers `feature_disabled`. Leaving or being
kicked gives `conversation.removed` (and `clan_kicked`), and the members who stay get a `member_left` / `member_kicked` line; joining again brings the room back with its whole history. In a clan room,
`online` in `members` is the state when the room was sent; live `presence.update` frames go to friends only.

**Postman collection and flow diagrams.** `postman/realtime-chat.postman_collection.json` runs the whole flow against a local stack: it makes its own developer, game, three players and a friendship, then checks the conversation, the history, paging, unread and the error cases (104 checks). The two things that need a WebSocket (sending messages and `conversation.read`) are two manual steps that show the exact frames to send in the Visualize tab; the collection description says how to run it. Folder 9 (TEAM-150) tries every `message.send` rule: it sets the game's rules through Platform (draft and Publish), puts `player.blocked` / `player.unblocked` on RabbitMQ (set the collection variables `rabbitUser` and `rabbitPassword`), and its STEP requests show the frames that give `rate_limited` (and the `1008` close), `feature_disabled`, `blocked`, `too_long`, `slow_mode`, `empty` and `not_member`. How everything flows, as UML diagrams: [`docs/realtime-flow.md`](realtime-flow.md).

**Postman collection for the clan member lines (TEAM-291).** `postman/realtime-clan-member-lines.postman_collection.json` (51 requests, 128 checks) makes its own developer, game (clans and clan chat on), four players and a clan, and checks that a join, a leave and a kick are stored in the clan chat as `member_joined`, `member_left` and `member_kicked` lines: the player as sender and an empty body, in the history and as `lastMessage` with `kind`, sent to the right members, never counted as unread, and a rejoiner gets the whole history. Two manual WebSocket steps (STEP A: Alice's and Bob's tabs before the joins; STEP B: Bob sends a text and a frame that tries to pass itself off as a member line, which is stored as `text`) show the exact frames in the Visualize tab. Folder 9 puts a repeated `clan.member_joined`, a copy of the same event and a `clan.member_left` for a player who is gone on RabbitMQ (set `rabbitUser` and `rabbitPassword`) and checks that none of them writes a second line.

**Conversation list, history and "read" (Postman).** After the chat above, for each player (use that player's token):

```
GET http://localhost:8083/api/v1/conversations                                           Authorization: Bearer <token>
GET http://localhost:8083/api/v1/conversations/<conversation id>/messages?limit=20       Authorization: Bearer <token>
GET http://localhost:8083/api/v1/conversations/<conversation id>/messages?before=<id of the oldest message>&limit=20
```

The list shows the other player's `lastMessage` and `unread` (the number of messages the *other* player sent that you have not read).
The messages come newest first; `hasMore` is `true` while older ones exist, then ask again with `before` set to the `id` of the
last message you got. To clear the unread count send, in that player's WebSocket tab:

```
{"type":"conversation.read","id":"r-1","data":{"conversationId":"<conversation id>","lastMessageId":"<id of the newest message>"}}
```

There is no reply; `GET /api/v1/conversations` now shows `unread: 0`. A message sent later makes it `1` again. Reading is remembered per player
and only moves forward. A player who is not in the conversation gets 403 `not_member` (REST) or an `error` frame with `not_member` (WebSocket).

Only the service-wide `REALTIME_CORS_ORIGINS` list is checked for the origin so far. Platform now stores a per-game
`allowedOrigins` list (set with `PUT /api/v1/games/{gameID}/allowed-origins`) and serves it, with the active config, on
`GET /internal/v1/games/{gameID}/config`; realtime does not read it yet (TEAM-142, TEAM-148).
