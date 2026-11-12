# cotd-results

Posts Trackmania Cup of the Day results for members of one or more clubs to a
Discord webhook. No bot account is needed: it uses a channel webhook and edits
its own messages in place. Messages use Discord embeds with a red stripe, and
each player gets their nation flag when the API provides a zone.

Each cup gets its own message. The message is posted as soon as the qualifier
closes (with every tracked player's division), then edited roughly once a
minute as division matches finish. Three crossplay cups are followed per day:

| Edition | Cup | Time (CET/CEST) |
| --- | --- | --- |
| 1 | Cup of the Day | 19:00 |
| 2 | Cup of the Night | 03:00 |
| 3 | Cup of the Morning | 11:00 |

The schedule comes from the API, not from hardcoded times, so daylight saving
changes and Nadeo schedule changes are handled automatically.

## What it does

1. Polls `GET /api/cups-of-the-day` and keeps the cups matching
   `cups.partitions` / `cups.editions`.
2. Reads the competition rounds and finds the qualifier challenge
   (`qualifierChallengeId`) and the round whose matches are the divisions.
3. Pages through the challenge leaderboard in pages of 100, filters it to club
   members, and derives each player's division:
   `division = (qualifierRank - 1) / matchSize + 1` (match size is 64 for COTD).
4. Posts the qualifier breakdown to Discord.
5. Polls the round's matches. When a division's match is complete, it fetches
   the match results and computes the overall position as
   `(division - 1) * matchSize + matchRank` (the API only provides the rank
   within the match).
6. Edits the same Discord message(s) when something changes. Once every tracked
   division is finished, it writes a JSON history file and stops polling that
   cup. The embed title becomes e.g. `2026-10-01 COTD #1 has finished`.

Before any division finishes, players are grouped by division and ordered by
qualifier rank. Once a division finishes its section switches to final order:
players are ordered by their finishing position within that division, and the
qualifier position is no longer shown. Divisions that are still racing keep the
qualifier-style view until they finish.

The unit of "division" is the match position: match position `0` is Division 1,
`1` is Division 2, and so on. Qualifier division assignment is rank-based,
which is how COTD seeds its matches.

## Setup

### 1. Nadeo service account

Create a service account at
<https://www.trackmania.com/player/service-account> (a dedicated server account
also works). Put its **login** and **password** in `config.toml`. Do not use
your Ubisoft email/password; the new service account flow is the supported way
to get API tokens.

### 2. OAuth app for display names

Create an OAuth app at <https://api.trackmania.com> (Confidential, client
credentials flow). Put the app's identifier/secret in `config.toml`. This is
only used to convert account IDs to names. Without it the tool still works, but
shows short account ID prefixes.

The app creation form asks for a redirect URI. This tool uses the client
credentials flow, which never redirects, so the value is unused — any valid
placeholder such as `https://localhost/oauth/callback` works.

### 3. Discord webhook

In Discord: Channel settings → Integrations → Webhooks → New Webhook → Copy
Webhook URL. Put it in `config.toml`.

### 4. Config

```sh
cp config.example.toml config.toml
# edit config.toml
```

Minimum config:

```toml
[discord]
webhook_url = "https://discord.com/api/webhooks/..."

[nadeo]
login = "..."
password = "..."
user_agent = "cotd-results/1.0 (+https://github.com/you/cotd-results)"

[clubs]
ids = [12345]
```

Everything else has sensible defaults. See `config.example.toml` for all
options, including labels, partitions, polling and catch-up windows.

### 5. Build and run

```sh
go build -o cotd-results.exe ./cmd/cotd-results
./cotd-results.exe -config config.toml
```

Useful flags:

- `-once` — run one poll cycle and exit (handy for testing credentials).
- `-dry-run` — print Discord messages to stdout and do not write state/history.
- `-test-webhook` — send a fake qualification message, wait 5 seconds, edit it
  with simulated final results, then exit. Only `discord.webhook_url` is
  required for this mode, so it works before the Nadeo credentials are set up.
- `-debug` — verbose logging.

To preview the embed style without touching the real data:

```sh
./cotd-results.exe -config config.toml -test-webhook
```

Do not put credentials in source control; `config.toml` and `data/` are
gitignored.

### Running continuously on Windows

The process is long-running (it polls in a loop). With Task Scheduler, create a
task with:

- Trigger: **At startup** (or **At log on**)
- Action: **Start a program**
  - Program: full path to `cotd-results.exe`
  - Arguments: `-config "C:\path\to\config.toml"`
  - Start in: the folder containing `config.toml`
- Settings: enable **Restart the task if it fails**, and disable
  **Stop the task if it runs longer than…**

Alternatively use a service wrapper such as NSSM. Because relative data paths
resolve next to `config.toml`, the working directory does not matter.

## Data files

A relative `settings.data_dir` is resolved next to `config.toml`, not the
current working directory, so scheduled tasks and service wrappers can start
the binary from anywhere.

- `data/state.json` — in-progress cup state and the Discord message IDs, so a
  restart edits existing messages instead of reposting them.
- `data/names.json` — cached display names.
- `data/history/YYYY-MM-DD_<competitionId>.json` — final club-member results
  for every completed cup.

If you change `discord.webhook_url`, the tool detects the new webhook ID and
reposts the current cup's messages to the new webhook.

## API details used

All Nadeo endpoints use a `NadeoLiveServices` token obtained with the service
account:

```
POST https://prod.trackmania.core.nadeo.online/v2/authentication/token/basic
Authorization: Basic <login:password>
{ "audience": "NadeoLiveServices" }
```

Then every request sends `Authorization: nadeo_v1 t=<accessToken>`.

| Purpose | Endpoint |
| --- | --- |
| Discover cups | `GET meet.trackmania.nadeo.club/api/cups-of-the-day?type=cotd&length=100&offset=0` |
| Competition rounds / qualifier ID | `GET /api/competitions/{id}/rounds` |
| Qualifier leaderboard (100/page) | `GET /api/challenges/{id}/leaderboard?length=100&offset=N` |
| Round matches (100/page) | `GET /api/rounds/{id}/matches?length=100&offset=N` |
| Match results (255/page) | `GET /api/matches/{matchId}/results?length=255&offset=N` |
| Club members (250/page) | `GET live-services.trackmania.nadeo.live/api/token/club/{clubId}/member?length=250&offset=N` |
| Display names (50 IDs) | `GET api.trackmania.com/api/display-names?accountId[]=...` |

Leaderboard entries include a `zone` such as
`World|Europe|France|Ile-de-France`. The third segment is the country; the tool
maps it to an ISO alpha-2 code and renders the corresponding flag emoji. If the
zone is missing or the country is unknown, the flag is simply omitted.

Notes:

- `/api/cup-of-the-day/current` only returns the crossplay cup that is
  currently active. The tool uses the cups list instead so it can see all three
  daily editions (and future ones) early.
- The cups list includes platform partitions (`pc`, `psn`, `xbl`, `luna`) and
  reruns. The default filter is `crossplay` + editions 1–3. Add partitions in
  config if you want those too.
- The qualifier can be thousands of players, so it is paged once per cup and
  then cached in state. Requests are throttled (`page_delay_ms`, default
  150 ms) and retried on `429`/`5xx`.
- Final standings are derived rather than returned by the API: match `position`
  is the division, and the overall place is
  `(division - 1) * 64 + rankWithinMatch`. The competition leaderboard endpoint
  is not populated for COTD, so the tool does not rely on it.

## Behaviour and edge cases

- **Bot restarts mid-cup:** state is loaded and polling resumes. If it restarts
  before the qualifier is processed, it processes it on the next tick. Cups that
  ended within `catchup_hours` are recovered; older ones are ignored.
- **No club member qualifies:** a short "No club members qualified" message is
  posted for that cup, so you still get three messages a day.
- **A player doesn't play their division match:** after the match is complete
  they are shown as `no result`, as is anyone still missing when the cup's
  scheduled end + `finalize_grace_seconds` passes.
- **Discord rate limits:** messages are only edited when their content changes,
  never on a fixed timer. `429` responses are respected.
- **Long member lists:** messages are split into multiple chunks under Discord's
  2000-character limit. Surplus chunks are deleted when the list shrinks.
- **Changing club membership:** membership is refreshed every
  `club_cache_hours` and captured at qualifier time, so mid-cup changes do not
  rewrite the player list.

## Troubleshooting

- `authenticate: HTTP 401` (or sometimes `HTTP 500` with `C-AA-00-01`) — wrong
  service account login/password, or you used your Ubisoft credentials instead
  of the service account credentials.
- `HTTP 403` — your user agent is missing/blocked, or the account is restricted.
  Set `nadeo.user_agent` to include a project name and contact.
- Names show as UUID prefixes — the OAuth app credentials are missing/invalid.
- Nothing posted — run with `-once -debug` and check that a cup is inside the
  active window (`lookahead_hours` before start, `catchup_hours` after end).
- Discord `404` on edit — the message was deleted manually; the tool reposts it.

## Development

```sh
go test ./...
go vet ./...
```

### Project layout

```
cmd/cotd-results/   command-line entry point
internal/api/       shared throttled HTTP client
internal/app/       polling loop and cup lifecycle
internal/config/    config.toml loading and validation
internal/discord/   webhook message sync
internal/geo/       zone -> country flag emoji
internal/model/     state and message types shared by packages
internal/nadeo/     Nadeo and OAuth API clients
internal/render/    Discord embed rendering
internal/store/     state, name cache, and history persistence
```
