# validator-alert-bot

Telegram bot that watches Cosmos-SDK validators and alerts subscribers about missed blocks, uptime drops and jailing events.

## Features

- Checks every subscribed validator's signing info every **10 minutes**
- Missed-block alerts are evaluated fresh each check against only the current and previous
  reading — a validator whose missed-blocks counter is merely staying high while trending down
  does not keep re-triggering the same alert; every message includes the current uptime % and
  missed-blocks count:
  - 🔴 **Critical** — missed blocks jumped by more than 100 in a single check, OR slashing-window
    uptime is currently below 80% (this one can keep firing every check while uptime stays that low —
    it's a real, ongoing risk, not a one-off event)
  - 🟡 **Missed Blocks Alert** — slashing-window uptime dropped by ≥ 1 percentage point since the
    last check, OR missed blocks increased by 50-100 in a single check
  - 🟢 **Recovering** — was previously alerting, and neither condition above holds anymore
- 🚨 Jailing / tombstoning alerts and ✅ unjail notices
- 💚 Health ping every **6 hours** with a per-validator status summary, so you know the bot itself is alive
- ⏰ Chain-upgrade watcher: tracks governance software-upgrade proposals per network — from voting period through passed — and warns **1 day** and **1-2 hours** before the estimated upgrade time (see below)
- Alert state is persisted in `./data`, so restarts do not re-send alerts
- Supports any network listed in [config/networks.json](config/networks.json); validators are identified by their bech32 `valcons` address
- Optional web dashboard: the same uptime/safety report as `/dashboard`, as an HTML page (see below)

## Bot commands

All commands work in group chats as well as DMs.

| Command | Description |
|---|---|
| `/subscribe <valcons addresses ...>` | Subscribe to alerts for one or more validators. Alerts are always sent by DM to the subscribing user, regardless of which chat `/subscribe` was run from — if that user has never messaged the bot privately, Telegram won't let it DM them, so alerts silently never arrive. Run it from a group and the confirmation message calls this out with a link to start a DM. |
| `/unsubscribe` | Remove all your subscriptions |
| `/uptime` | Show window size, currently missed blocks, uptime % and 🟢/🔴 safety per validator you're subscribed to |
| `/dashboard` | Show uptime % and safety for **every** validator configured in `config/validator_aliases.json`, grouped by network — not just your own subscriptions. Safety here is based on the chain's real `min_signed_per_window` slashing param rather than a flat percentage, so it's a more accurate jail-risk signal than `/uptime`'s 🔴 |
| `/upgrades` | List full details (proposal, target height, ETA) for every currently tracked chain-upgrade |
| `/help` | Show help |

Thresholds and intervals are constants in [config/config.go](config/config.go).

## Upgrade watcher

Every **15 minutes** the bot polls each network in [config/networks.json](config/networks.json) for
governance proposals of the software-upgrade type — both **voting period** and **passed** —
(checking both the modern `gov/v1` message format and the legacy `gov/v1beta1` proposal-content
format, plus the `x/upgrade` module's own `current_plan` query as a fallback for passed plans).
Voting-period proposals show up in `/upgrades` right away as an early heads-up (🗳, not yet
confirmed — the vote could still fail), but push alerts and ETA calculations only start once a
proposal has actually passed, since only then is the target height confirmed on-chain. For each
passed upgrade the bot tracks the target height and estimates time-to-upgrade from a
self-measured average block time (sampled each check cycle, so the estimate adapts to each
chain's real block speed and survives restarts). Subscribers are notified based on the networks
of their subscribed validators:

- ⏰ **Upgrade Incoming** — fires once ~24 hours out and again once ~1-2 hours out (passed upgrades only)
- ✅ **Upgrade Height Reached** — the target height has been passed; the upgrade is removed from state
- ⚠️ **Upgrade Cancelled** — a cancellation was observed via governance; the upgrade is removed from state

A voting-period proposal that gets rejected or fails is quietly dropped from tracking (no alert,
since none was ever sent for it). Upgrade-watcher state is persisted to `./data/upgrades.json`.

## Validator aliases (optional)

Alerts identify validators by their `valcons` address. To show a friendly `Moniker (network)`
label instead, copy
[config/validator_aliases.example.json](config/validator_aliases.example.json) to
`config/validator_aliases.json` and fill in your validators. When an alias includes the
`network` and `validator_address` (valoper) fields, alerts, health checks and `/uptime` also
include a `mintscan.io/<network>/validators/<valoper>` link so you can verify missed blocks on
the explorer. The file is gitignored and fully optional — without it the bot works the same,
alerts just show the raw address and skip the explorer link.

`/dashboard` is driven entirely by this file — it's how the bot knows which validators to report
on regardless of who has subscribed. Without `config/validator_aliases.json` (or with it empty),
`/dashboard` has nothing to show.

## Web dashboard (optional)

The bot can also serve the same report `/dashboard` shows as a small self-refreshing HTML page
(no external CSS/JS, plain `net/http`), listing every validator in
`config/validator_aliases.json` in a single table (network as its own column) with an uptime
bar and 🟢/🔴 safety per row.

It's disabled by default and only starts if **both** `WEB_USERNAME` and `WEB_PASSWORD` are set —
visiting the page prompts a login form (session cookie, 24h expiry) rather than a browser Basic
Auth popup; `/logout` ends the session. Set `WEB_LISTEN_ADDR` to change the listen address
(default `:8080`).

```sh
export WEB_USERNAME=admin
export WEB_PASSWORD=<pick something>
```

Then visit `http://localhost:8080` (or whatever `WEB_LISTEN_ADDR`/port you configured) and
authenticate with those credentials.

## Running

```sh
export BOT_API_KEY=<telegram bot token from @BotFather>
go build -o validator-alert-bot .
./validator-alert-bot
```

Runtime state is written to `./data` (created automatically).

### Docker Compose (recommended)

```sh
echo 'BOT_API_KEY=<token>' > .env
# optional, to enable the web dashboard:
echo 'WEB_USERNAME=admin' >> .env
echo 'WEB_PASSWORD=<pick something>' >> .env
docker compose up -d --build
docker compose logs -f
```

Missed-block state persists in `./data`, and `./config` is mounted read-only so
network endpoints and aliases can be changed with just a restart, no rebuild. The web
dashboard, if enabled, is published on port `8080` (override with `WEB_PORT` in `.env`).

### Docker

```sh
docker build -t validator-alert-bot .
docker run -d --name validator-alert-bot \
  -e BOT_API_KEY=<token> \
  -e WEB_USERNAME=admin \
  -e WEB_PASSWORD=<pick something> \
  -p 8080:8080 \
  -v $(pwd)/data:/app/data \
  --restart unless-stopped \
  validator-alert-bot
```

### systemd

```ini
[Unit]
Description=Cosmos validator alert bot
After=network-online.target

[Service]
WorkingDirectory=/opt/validator-alert-bot
Environment=BOT_API_KEY=<token>
# optional, to enable the web dashboard:
Environment=WEB_USERNAME=admin
Environment=WEB_PASSWORD=<pick something>
ExecStart=/opt/validator-alert-bot/validator-alert-bot
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```
