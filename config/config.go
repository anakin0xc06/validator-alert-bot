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
	// YellowMissedBlocksLimit cumulative missed blocks in the current missing episode that triggers a yellow alert
	YellowMissedBlocksLimit = 20
	// CriticalMissedBlocksLimit cumulative missed blocks in the current missing episode that triggers a critical alert
	CriticalMissedBlocksLimit = 100
	// CriticalConsecutiveChecks consecutive checks at/above YellowMissedBlocksLimit that force escalation to critical
	CriticalConsecutiveChecks = 2
	// RecoveryMissedBlocksDrop the missed-blocks counter must drop by at least this much since the previous check before a recovery alert fires
	RecoveryMissedBlocksDrop = 50
	// CriticalWindowPercent missed percentage of the slashing window that triggers a critical alert
	CriticalWindowPercent = 50
	// CheckIntervalSeconds how often validators are checked for missed blocks
	CheckIntervalSeconds = 300
	// HealthCheckIntervalHours how often the bot reports that it is alive
	HealthCheckIntervalHours = 6
	// UpgradeCheckIntervalSeconds how often the bot polls governance proposals and block heights for scheduled upgrades
	UpgradeCheckIntervalSeconds = 900
	// UpgradeDayWarningHours how long before the estimated upgrade time the first warning fires
	UpgradeDayWarningHours = 24
	// UpgradeHourWarningHours how long before the estimated upgrade time the final warning fires
	UpgradeHourWarningHours = 2
)
