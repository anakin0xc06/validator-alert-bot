package config

import "os"

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
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
