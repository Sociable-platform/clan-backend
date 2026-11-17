# Running everything locally

One set of steps to run the whole platform on your computer: the backend, Studio, the SDK and
the demo game. Each repository's README has more detail; this page is the order to do it in and
how the pieces connect.

These steps were put together from the four READMEs and the configuration files. If a command
here disagrees with what you see, the repository's own README wins; please fix this page.

## What runs where

| Part | Repository | Address | Started with |
|---|---|---|---|
| PostgreSQL | clan-backend | `localhost:5432` | `docker compose up` |
| RabbitMQ | clan-backend | `localhost:5672`, management UI `http://localhost:15672` | `docker compose up` |
| platform | clan-backend | `http://localhost:8081` | `docker compose up` |
| social | clan-backend | `http://localhost:8082` | `docker compose up` |
| realtime | clan-backend | `http://localhost:8083` | `docker compose up` |
| Studio | clan-frontend | `http://localhost:5173` | `npm run dev` |
| SDK | clan-sdk | `http://127.0.0.1:18080/v1/clan-sdk.js` | `docker compose up` |
| Demo game (web) | demo-game | `http://localhost:5174` | `npm run dev` (see step 5) |
| Demo game (its server) | demo-game | `http://localhost:3001` | the same command |

The two ports that matter are **5173 for Studio** and **5174 for the game**. The backend only
accepts browser calls from those addresses: platform from 5173, social and realtime from 5174
(`PLATFORM_CORS_ORIGINS`, `SOCIAL_CORS_ORIGINS`, `REALTIME_CORS_ORIGINS`).

## What you need

- Docker Desktop
- Node.js 22.13 or newer
- Go 1.26 or newer, only if you want to run the backend's tests or tools
- Git Bash on Windows, for the `openssl` commands
- For the SDK only: a read-only GitLab deploy token with `read_package_registry`, to install
  `@clan/ui`. Ask a teammate for one. Without it, skip step 4 and use the hosted SDK.

## 1. Backend

In `clan-backend`:

```bash
cp .env.example .env
```

Edit `.env` and set the five passwords (`POSTGRES_PASSWORD`, `PLATFORM_DB_PASSWORD`,
`SOCIAL_DB_PASSWORD`, `REALTIME_DB_PASSWORD`, `RABBITMQ_PASSWORD`). Use letters and digits only.

Create the key platform signs tokens with. Do this before the first start: if a key file is
missing, Docker makes an empty folder with its name and the services do not start.

```bash
mkdir -p secrets
openssl genpkey -algorithm ed25519 -out secrets/platform_jwt_private.pem
openssl pkey -in secrets/platform_jwt_private.pem -pubout -out secrets/platform_jwt_public.pem
chmod 644 secrets/platform_jwt_private.pem secrets/platform_jwt_public.pem
```

Start it:

```bash
docker compose up --build
```

Check that the three services are up. Each should answer `{"service":"...","status":"ok"}`:

```bash
curl http://localhost:8081/health
curl http://localhost:8082/health
curl http://localhost:8083/health
```

Three settings in `.env` that are worth knowing about locally:

| Variable | Local value | Why |
|---|---|---|
| `PLATFORM_COOKIE_SECURE` | `false` | The refresh cookie is otherwise sent over HTTPS only, and local Studio is plain HTTP |
| `PLATFORM_EMAIL_VERIFICATION` | `false` | Registration then needs no emailed code |
| `PLATFORM_SMTP_*` | empty | Without a mail server, password-reset emails are written to platform's log |

## 2. Studio

In `clan-frontend`:

```bash
cp .env.example .env     # VITE_PLATFORM_URL=http://localhost:8081
npm install
npm run dev
```

Open `http://localhost:5173`. Keep this port: platform refuses browser calls from any other.

## 3. A developer, a game and a key

In Studio:

1. Create an account and sign in.
2. Open **Your games** and register a game. Copy its **Game ID**.
3. Press **Customize widget**, then **Publish**. The SDK shows nothing until a version is
   published.
4. Press **API keys**, then **Create API key**. Copy the key; it is shown once.

For the admin panel (`http://localhost:5173/admin/login`), create an admin with the tool; there
is no register page for admins. See `operations.md`, "Create an admin", and use the local
database address.

## 4. SDK

In `clan-sdk`, with the deploy token in your terminal as `GITLAB_NPM_TOKEN` (the clan-sdk README
shows how to enter it without it landing in the command history):

```bash
docker compose build sdk
docker compose up -d sdk
```

The script is then at `http://127.0.0.1:18080/v1/clan-sdk.js`.

No token? Skip this step and point the game at the hosted SDK in step 5
(`VITE_CLAN_SDK_URL=https://sdk.<host>/v1/clan-sdk.js`).

## 5. Demo game

In `demo-game`:

```bash
cp .env.example .env
npm install
```

In `.env`, set what connects the game to your local platform:

```dotenv
APP_ORIGIN=http://localhost:5174,http://127.0.0.1:5174,http://localhost:3001,http://127.0.0.1:3001
CLAN_PLATFORM_URL=http://localhost:8081
CLAN_GAME_API_KEY=<the key from step 3>
VITE_GAME_ID=<the Game ID from step 3>
```

`CLAN_GAME_API_KEY` is a secret: it stays in `.env`, which git ignores. The `VITE_*` addresses
already point at the local services and the local SDK.

Start the game's server and its web page, the web page on port 5174, in two terminals:

```bash
npm run dev:api
npm run dev -w frontend -- --port 5174
```

Open `http://localhost:5174`, create a player, and open the clans panel.

## Try it with two players

Open the game in a normal window and in a private window, and create a player in each. Then:

- one creates a clan, the other joins it;
- both write in the clan chat;
- one sends the other a friend request.

To check the whole realtime path without a browser, the backend has an end-to-end tool. With
the stack running, in `clan-backend`:

```bash
go run ./tools/e2e
```

It plays a developer and two players through register, publish, friends, direct messages and
clan chat, and exits with 0 when every step passed. It leaves its test data in the local database.

## When something does not work

| You see | Likely cause |
|---|---|
| A service keeps restarting, with a key error in its log | The key files were missing at the first start. Remove the empty `secrets/platform_jwt_*.pem` folders, create the keys, start again |
| Studio: every request fails with a CORS error | Studio is not on port 5173, or `VITE_PLATFORM_URL` is wrong |
| Studio: signed out after a page reload | `PLATFORM_COOKIE_SECURE` is not `false` |
| The game: the widget shows an error, the console has a CORS error | The game is not on port 5174 |
| The game: its own requests are refused | `APP_ORIGIN` does not include `http://localhost:5174` |
| The widget is empty or shows a default look | The game's widget was not published, or `VITE_GAME_ID` is another game's |
| `players/auth` answers 401 in the game server's log | `CLAN_GAME_API_KEY` is wrong, revoked, or belongs to another game |
| Database errors after pulling new code | A new migration; `docker compose up` applies it. To start clean: `docker compose down -v` (deletes all local data) |

## Stopping

```bash
docker compose down        # in clan-backend and in clan-sdk; data is kept
docker compose down -v     # in clan-backend: also deletes the local database
```
