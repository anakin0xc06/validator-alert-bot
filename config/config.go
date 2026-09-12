package config

import (
	"log"
	"os"
	"strconv"
)

var (
	BOT_API_KEY          = os.Getenv("BOT_API_KEY")
	SubscribersFile      = "./data/subscribers.json"
	ValidatorsFile       = "./data/validators.json"
	StateFile            = "./data/state.json"
	UpgradesFile         = "./data/upgrades.json"
	NetworksFile         = "./config/networks.json"
	ValidatorAliasesFile = "./config/validator_aliases.json"

	// WebListenAddr is the address the web dashboard listens on
	WebListenAddr = envOrDefault("WEB_LISTEN_ADDR", ":8080")
	// WebUsername/WebPassword gate the web dashboard behind HTTP Basic Auth.
	// The web server does not start unless both are set.
	WebUsername = os.Getenv("WEB_USERNAME")
	WebPassword = os.Getenv("WEB_PASSWORD")

	// MissedBlocksChatID/UpgradesChatID/JailedChatID are the fixed Telegram
	// chats (DM, group or channel) each alert type is posted to, in place of
	// DMing every subscriber individually. A chat left unset (0) means that
	// alert type is dropped (logged, not sent) rather than silently lost.
	MissedBlocksChatID = parseChatID("MISSED_BLOCKS_CHAT_ID")
	UpgradesChatID     = parseChatID("UPGRADES_CHAT_ID")
	JailedChatID       = parseChatID("JAILED_CHAT_ID")
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseChatID(envVar string) int64 {
	v := os.Getenv(envVar)
	if v == "" {
		return 0
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		log.Fatalf("Invalid %s %q: %v", envVar, v, err)
	}
	return id
}

const (
	// NormalMissedBlocksDelta missed-blocks increase in a single check that triggers a normal alert (up to CriticalMissedBlocksDelta)
	NormalMissedBlocksDelta = 50
	// CriticalMissedBlocksDelta missed-blocks increase in a single check above which the alert is critical
	CriticalMissedBlocksDelta = 100
	// CriticalUptimePercent window uptime percentage below which an alert is always critical, regardless of delta
	CriticalUptimePercent = 80.0
	// UptimeDropAlertPercent minimum uptime percentage-point drop since the last check that triggers a normal alert
	UptimeDropAlertPercent = 1.0
	// CriticalWindowPercent missed percentage of the slashing window that triggers a critical alert
	CriticalWindowPercent = 50
	// CheckIntervalSeconds how often validators are checked for missed blocks
	CheckIntervalSeconds = 600
	// HealthCheckIntervalHours how often the bot reports that it is alive
	HealthCheckIntervalHours = 6
	// UpgradeCheckIntervalSeconds how often the bot polls governance proposals and block heights for scheduled upgrades
	UpgradeCheckIntervalSeconds = 900
	// UpgradeDayWarningHours how long before the estimated upgrade time the first warning fires
	UpgradeDayWarningHours = 24
	// UpgradeHourWarningHours how long before the estimated upgrade time the final warning fires
	UpgradeHourWarningHours = 2
)
