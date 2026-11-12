# cotd-results

Posts Trackmania Cup of the Day results for members of one or more clubs to a
Discord webhook.

Each cup gets its own message. The message is posted as soon as the qualifier
closes and shows each player's division, then edited once a
as soon as a division match finishes. Three crossplay cups are followed per day:

| Edition | Cup                | Time (CET/CEST) |
| ------- | ------------------ | --------------- |
| 1       | Cup of the Day     | 19:00           |
| 2       | Cup of the Night   | 03:00           |
| 3       | Cup of the Morning | 11:00           |

## Setup

### 1. Nadeo service account

Create a service account at
<https://www.trackmania.com/player/service-account> (a dedicated server account _might_ work too). Put its **login** and **password** in `config.toml`.

> [!IMPORTANT]
> I recommend creating a brand new account for this and **NOT** using your personal Ubisoft account.

### 2. OAuth app for display names

Create an OAuth app at <https://api.trackmania.com> (Keep the type as **"Confidential"**). Put the app's identifier/secret in `config.toml`. This is
only used to convert account IDs to names. Without it the tool still works, but
shows short account ID prefixes instead.

The app creation asks for a redirect URI. This app doesn't redirect, so the value is unused. Any valid placeholder such as `https://localhost/oauth/callback` works.

### 3. Discord webhook

In Discord: Channel settings → Integrations → Webhooks → New Webhook → Copy
Webhook URL. Put it in `config.toml`.

### 4. Config

Rename or copy `config.example.toml` to `config.toml` and edit it.

```sh
cp config.example.toml config.toml
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

### 5. Build and run

```sh
go build -o cotd-results.exe ./cmd/cotd-results
./cotd-results.exe
```

Useful flags:

- `-once` - run one poll cycle and exit.
- `-dry-run` - print Discord messages to stdout and do not write state/history.
- `-test-webhook` - sends a fake qualification message, wait 5 seconds, edits it
  with simulated final results, then exits. Only `discord.webhook_url` is
  required for this mode.
- `-debug` - verbose logging.

To preview the embed style without touching the real data:

```sh
./cotd-results.exe -test-webhook
```

## Data files

- `data/state.json` - in-progress cup state and the Discord message IDs, so a
  restart edits existing messages instead of reposting them.
- `data/names.json` - cached display names.
- `data/history/YYYY-MM-DD_<competitionId>.json` - final club-member results
  for every completed cup.

If you change `discord.webhook_url`, the tool detects the new webhook ID and
reposts the current cup's messages to the new webhook.

## Notes

- The cups list includes platform partitions (`pc`, `psn`, `xbl`, `luna`) and
  reruns. The default filter is `crossplay` + editions 1–3.
- The qualifier can be thousands of players, so it is paged once per cup and
  then cached in state. Requests are throttled (`page_delay_ms`, default
  150 ms) and retried on `429`/`5xx`.

## Development

```sh
go test ./...
go vet ./...
```
