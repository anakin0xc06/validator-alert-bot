# validator-alert-bot

Telegram bot that watches Cosmos-SDK validators and alerts subscribers about missed blocks, jailing events and slashing risk.

## Features

- Checks every subscribed validator's signing info every **5 minutes**
- Graded missed-block alerts:
  - 🟡 **Yellow** — missed ≥ 10 new blocks since the last check
  - 🔴 **Red** — missed ≥ 50 new blocks since the last check
  - 🚨 **Critical** — missing more than 50% of the network's slashing window (jail/slash risk)
  - 🟢 **Recovering** — missed-block counter is going back down after an alert
- 🚨 Jailing / tombstoning alerts and ✅ unjail notices
- 💚 Health ping every **6 hours** with a per-validator status summary, so you know the bot itself is alive
- Alert state is persisted in `./data`, so restarts do not re-send alerts
- Supports any network listed in [config/networks.json](config/networks.json); validators are identified by their bech32 `valcons` address

## Bot commands

| Command | Description |
|---|---|
| `/subscribe <valcons addresses ...>` | Subscribe to alerts for one or more validators |
| `/unsubscribe` | Remove all your subscriptions |
| `/uptime` | Show window size, currently missed blocks, uptime % and 🟢/🔴 safety per validator |
| `/help` | Show help |

Thresholds and intervals are constants in [config/config.go](config/config.go).

## Validator aliases (optional)

Alerts identify validators by their `valcons` address. To show a friendly `Moniker (network)`
label instead, copy
[config/validator_aliases.example.json](config/validator_aliases.example.json) to
`config/validator_aliases.json` and fill in your validators. When an alias includes the
`network` and `validator_address` (valoper) fields, alerts, health checks and `/uptime` also
include a `mintscan.io/<network>/validators/<valoper>` link so you can verify missed blocks on
the explorer. The file is gitignored and fully optional — without it the bot works the same,
alerts just show the raw address and skip the explorer link.

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
docker compose up -d --build
docker compose logs -f
```

Missed-block state persists in `./data`, and `./config` is mounted read-only so
network endpoints and aliases can be changed with just a restart, no rebuild.

### Docker

```sh
docker build -t validator-alert-bot .
docker run -d --name validator-alert-bot \
  -e BOT_API_KEY=<token> \
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
ExecStart=/opt/validator-alert-bot/validator-alert-bot
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```
