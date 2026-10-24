# HappyX.Api

REST API for HappyX: members of a Discord server answer a daily mood survey (1-5, with an
optional message) and the server admin receives a weekly or monthly report.

Go, clean architecture, PostgreSQL. One API serves many Discord servers ("guilds") without
mixing their data.

## Layout

```
cmd/api                      entry point (config, wiring, graceful shutdown)
internal/domain              entities and rules (Guild, Member, MoodEntry, Report); no dependencies
internal/usecase             application logic + repository interfaces (ports)
internal/repository/postgres PostgreSQL adapters (pgx) and embedded migrations runner
internal/transport/http      JSON REST handlers and API key middleware
migrations                   SQL migrations, applied automatically on startup
```

Dependencies point inwards: `transport` and `repository` depend on `usecase`, which depends on `domain`.

## Multi-server isolation

The Discord guild ID is the tenant key. Every route that touches tenant data is under
`/v1/guilds/{guildID}`, and every query on members and moods filters by that guild. The same
Discord user in two servers is two separate members with separate histories. See
`TestGuildIsolation`.

## Anonymity

Guilds are anonymous by default: reports include messages but never who wrote them. An admin can
turn this off with `PATCH /v1/guilds/{guildID}` `{"anonymous": false}`.

Each answer remembers whether the guild was anonymous when it was given. Turning anonymity off
only names the answers given from then on; earlier answers stay anonymous for good. A member who
answers the same day again after the switch replaces their answer, which is then named. Turning
anonymity back on hides every author again.

Kudos are the exception: they are public thanks posted in the server with both names, so reports
list them with names whatever the setting.

### Working days

Each guild has working days (`working_days`, Monday to Friday by default). The bot only sends the
daily survey on those days. Reports leave out non-working days unless someone answered on one, and
a member's streak isn't broken by them.

### Custom survey message

Admins can write their own text for the daily survey (`survey_message`, at most 500 characters).
It is sent as written, in the language it was written in; empty means the default text in the
guild's language. It may use these variables, which the bot fills in for each person:
`{{user.name}}`, `{{user.mention}}`, `{{user.streak}}`, `{{guild.name}}`, `{{date}}` and
`{{weekday}}`. Any other `{{...}}` is rejected with 400, so a typo never reaches members. The
anonymity notice, the mood buttons and «Don't ask me again» are always added by the bot below it.

### «Who's who?» game

A weekly icebreaker, off by default (`games_enabled`). On Monday the bot sends every member who
takes part a question in the guild's language and starts the round (`POST .../game/rounds`).
Members answer until Wednesday, from the DM or the web. From Thursday to Sunday they guess on the
web who wrote each answer; a round needs at least 3 answers. The authors and the ranking (most
right guesses, then fewest guesses) come out once the week is over, so game answers are never
anonymous and members are told so. Members who paused HappyX in the guild don't get the question,
can't play, and their answers and guesses drop out of the round.

From Thursday to Sunday members can also propose next week's question, one each (proposing again
replaces it), from the bot's Thursday DM or the web. On Monday one of them is drawn at random for
the round, whatever question the bot sent; with no proposals the bot's question is used. Who
proposed it stays secret until the results, when it comes out with the authors.

## Credentials

All secrets come from environment variables; nothing is read from files in the repo.

| Variable | Purpose |
| --- | --- |
| `DATABASE_URL` | PostgreSQL connection string |
| `API_KEY_HASHES` | Comma separated SHA-256 hex digests of the API keys allowed to call `/v1` |
| `HTTP_ADDR` | Listen address, default `:8080` |
| `DISCORD_CLIENT_ID` | Discord application ID. Set it to enable the web dashboard login; the next three are then required |
| `DISCORD_CLIENT_SECRET` | Discord application OAuth2 secret |
| `WEB_URL` | Public URL of the dashboard, e.g. `https://happyx.example.com` |
| `SESSION_SECRET` | At least 32 random characters to sign dashboard sessions (`openssl rand -hex 32`) |

Clients (for now, only the Discord bot) send `Authorization: Bearer <key>`. The API stores only
the hash, so a leaked config does not leak a usable key. Give each client its own key so it can
be rotated on its own:

```sh
KEY=$(openssl rand -hex 32); echo "key: $KEY"; echo -n "$KEY" | sha256sum
```

Put the key in the bot's environment and the hash in `API_KEY_HASHES`. Locally use a git-ignored
`.env` (see `.env.example`); in production use the host's secret store.

## Web dashboard

People log in to [HappyX.Web](https://github.com/moixblau/HappyX.Web) with Discord OAuth2 (scopes
`identify` and `guilds`). The dashboard's nginx forwards `/api/web/...` to this API's `/web/...`,
so the API itself doesn't need to be public. In the Discord application (OAuth2 > Redirects) add
`<WEB_URL>/api/web/auth/callback`.

Each user has one of these levels in a server:

- **owner**: the main admin (who receives the reports), and the server's owner and Discord
  Administrators. Everything below, plus choosing the main admin and who else has access, and
  deleting the server.
- **manage**: reports and the survey settings. Given to users or Discord roles by an owner.
- **view**: the dashboard and reports only. Given to users or Discord roles by an owner.

Grants only count while the person is still in the server; the bot keeps each member's roles and
the server's roles in sync. The `@everyone` role can't be granted. Discord permissions are read at login; the session is an
HMAC-signed HttpOnly cookie that lasts 12 hours and the API keeps no session state. The Discord
access token is used once at login and never stored. Reports keep the server's anonymity
setting. Servers a user can't see answer 404, like servers without HappyX.

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/web/auth/login` | redirects to Discord |
| GET | `/web/auth/invite` | redirects to Discord to add the bot to a server (scopes `bot applications.commands`; View Channel, Send Messages and Embed Links for kudos). No login needed |
| GET | `/web/auth/callback` | Discord redirects here; sets the session and goes back to `WEB_URL` |
| POST | `/web/auth/logout` | 204 |
| GET | `/web/me` | `{user, guilds, games}`: the logged-in user, the servers they can see (each with its `access` level), and the servers where they can play (`discord_guild_id`, `name`) |
| GET | `/web/me/moods` | `?from=&to=` (default: last 30 days). The user's own answers in every server, one entry per server with `responses`, `average`, `streak` and `entries` |
| GET | `/web/guilds/{guildID}/report` | same query and body as the `/v1` report |
| GET | `/web/guilds/{guildID}/game` | `{current, previous}`: this week's round and last week's, as the member sees them (authors only once finished). 404 unless games are on and the user takes part |
| PUT | `/web/guilds/{guildID}/game/answer` | `answer` (1-200): answer this week's question, Monday to Wednesday |
| PUT | `/web/guilds/{guildID}/game/guesses` | `{"guesses": {answerID: userID}}`: replaces the user's guesses, Thursday to Sunday. 204 |
| PUT | `/web/guilds/{guildID}/game/proposal` | `question` (1-200): propose next week's question, Thursday to Sunday. The game page's `proposal` is `{open, week, mine, count}`; rounds carry `proposed`, `my_question` and, once finished, `proposed_by` |
| PATCH | `/web/guilds/{guildID}/settings` | any of `anonymous`, `timezone`, `survey_hour`, `report_frequency`, `language`, `games_enabled`, `working_days`, `survey_message` (see «Custom survey message»; `""` goes back to the default). Same rules as the `/v1` PATCH; the main admin changes in `/access`. Needs **manage** and `Content-Type: application/json`; 403 for **view** |
| GET | `/web/guilds/{guildID}/access` | `{admin_discord_user_id, grants, roles, members}`: `grants` are `{kind: "user"\|"role", id, level: "view"\|"manage"}`; `roles` (`id`, `name`, `color`, `position`, highest first) and `members` (still in the server, by name) are what can be picked. Needs **owner** |
| PUT | `/web/guilds/{guildID}/access` | `admin_discord_user_id` and the full `grants` list (at most 50). The admin and granted users must be in the server, roles must exist. Needs **owner** and `Content-Type: application/json` |
| DELETE | `/web/guilds/{guildID}` | `confirm_name` (the server's name): deletes the server and all its data (members, moods, kudos, game, dispatches); the bot then leaves it. Needs **owner** and `Content-Type: application/json`. 204 |
| GET | `/web/guilds/{guildID}/export` | downloads the server's data as JSON (`happyx-server-data`): settings, members, survey answers, kudos and «Who's who?» rounds. Needs **owner**, and the same anonymity as the dashboard (see below) |
| GET | `/web/me/data` | downloads everything HappyX keeps about the logged-in user as JSON (`happyx-personal-data`): per server their membership, answers, kudos given and received, game answers, guesses and proposals, and which servers' reports they receive |
| DELETE | `/web/me/data` | `confirm_name` (the user's username): erases the user's answers, kudos (given and received) and game activity in every server, and pauses the survey for them everywhere. Their member rows stay, without a username, so the bot doesn't sign them up again. Needs `Content-Type: application/json`. 204 |

Any logged-in user can read their own answers (`/web/me/moods`), whether or not they administer
the server: they are the author, so anonymity doesn't hide anything from them. The streak counts
consecutive days answered up to today in the server's timezone; not having answered yet today
doesn't break it.


Personal data (GDPR): every logged-in user can download (`/web/me/data`) and erase
(`DELETE /web/me/data`) their own data. A server export never tells its managers more than the
dashboard does: a survey answer names its author only if the server is not anonymous now, the
answer was given while it wasn't either, and the author still takes part; anonymous answers are
sorted by content within each day. Game answers appear once guessing starts and their authors once
the round is finished. Nightly database backups still hold erased data until they rotate.

`/web` routes use the session cookie and `/v1` routes use API keys; neither works on the other.

## API

| Method | Path | Body | Notes |
| --- | --- | --- | --- |
| GET | `/health` | | public |
| GET | `/v1/guilds` | | active guilds (for the bot's scheduler) |
| POST | `/v1/guilds` | `discord_guild_id`, `name`, `admin_discord_user_id` | register on join; idempotent, keeps settings and the admin of a known server |
| GET | `/v1/guilds/{guildID}` | | |
| PATCH | `/v1/guilds/{guildID}` | any of `anonymous`, `timezone`, `survey_hour`, `report_frequency` (`weekly`/`monthly`), `language` (`en`, `es`, `pt-BR`…; default `en`), `admin_discord_user_id`, `kudos_channel_id` (`""` clears it), `games_enabled`, `working_days` (ISO days, `1` Monday to `7` Sunday, at least one; default `[1,2,3,4,5]`) | |
| DELETE | `/v1/guilds/{guildID}` | | bot removed: stops surveys and hides the server from the dashboard. Data is kept 30 days in case the bot is added back, then deleted |
| GET | `/v1/removals` | | Discord IDs of the servers deleted from the dashboard that the bot must leave |
| DELETE | `/v1/removals/{guildID}` | | the bot has left the server. Until then `POST /v1/guilds` for it answers 410 |
| GET | `/v1/guilds/{guildID}/members` | | active members; `?with=streak` adds each one's `streak` (days in a row answering, omitted when 0) |
| PUT | `/v1/guilds/{guildID}/members` | `members`: up to 1000 `{discord_user_id, username, role_ids}` | bulk sync in one transaction: creates or refreshes each member's name and roles, keeping everyone's opt-in state |
| PUT | `/v1/guilds/{guildID}/members/{userID}` | `username`, `active` (optional), `role_ids` (optional) | create or rename; `active` omitted keeps an existing member's opt-in state, `true` opts in; `role_ids` omitted keeps their Discord roles |
| DELETE | `/v1/guilds/{guildID}/members/{userID}` | | opt out; `?left=true` when they left the server, which also ends their dashboard access |
| GET | `/v1/guilds/{guildID}/members/{userID}/access` | | `{level}`: `none`, `view`, `manage` or `owner`, for the bot's admin commands. `?discord_admin=true` when Discord says they own the server or are an Administrator |
| PUT | `/v1/guilds/{guildID}/roles` | `roles`: `[{id, name, color, position}]` | replaces the server's roles; grants to roles that are gone are dropped. 204 |
| PUT | `/v1/guilds/{guildID}/members/{userID}/mood` | `mood` (1-5), `message` (optional, ≤500), `day` (optional `YYYY-MM-DD`) | one answer per day; answering again replaces it |
| GET | `/v1/guilds/{guildID}/report` | `?from=YYYY-MM-DD&to=YYYY-MM-DD` | without a range: last complete week or month |
| POST | `/v1/guilds/{guildID}/kudos` | `from_discord_user_id`, `to_discord_user_id`, `message` (1-300) | 201 with the kudos and both usernames. Both must be known members (paused is fine), not the same person; at most 10 per giver per day (429) |
| POST | `/v1/guilds/{guildID}/game/rounds` | `week` (a Monday, `YYYY-MM-DD`), `question` (1-300) | starts the week's round, Monday to Wednesday, with a proposed question if there are any (`proposed`); idempotent (keeps the first question). 409 when games are off or answering is over |
| GET | `/v1/guilds/{guildID}/game/rounds/{week}` | | `phase` (`answering`/`guessing`/`finished`), `answers` count, `guessing_starts`, `last_day` |
| PUT | `/v1/guilds/{guildID}/game/rounds/{week}/answers/{userID}` | `answer` (1-200) | replaces an earlier answer. 403 for paused members, 409 once answering is over |
| PUT | `/v1/guilds/{guildID}/game/proposals/{userID}` | `question` (1-200) | proposes next week's question, replacing an earlier one; returns its `week`. 403 for paused members, 409 Monday to Wednesday |
| POST | `/v1/guilds/{guildID}/dispatches` | `kind` (`survey`/`report`/`game`/`game_guess`), `period` (`YYYY-MM-DD`) | 201: go ahead and send; 409: already sent. Makes scheduled sends exactly-once across restarts |

Errors are `{"error": "..."}` with 400 (validation), 401, 403 (not a member), 404, 409 (inactive guild, dispatch already claimed, games off or game step closed), 429 (kudos limit).

The report also has `kudos`: `count`, `givers`, `receivers` and up to five `highlights` (the
latest, newest first, each with `day`, both IDs and usernames, `message` and `created_at`).

## Development

```sh
docker run -d --name happyx-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=happyx_test -p 55432:5432 postgres:18-alpine
export TEST_DATABASE_URL='postgres://postgres:test@localhost:55432/happyx_test?sslmode=disable'
go test -p 1 ./...
```

Database tests are skipped when `TEST_DATABASE_URL` is not set. To run the whole stack, use the
`docker-compose.yml` in [HappyX](https://github.com/moixblau/HappyX).
