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
)

const (
	// MissedBlocksLimit new missed blocks per check interval that triggers a yellow alert
	MissedBlocksLimit = 10
	// RedMissedBlocksLimit new missed blocks per check interval that triggers a red alert
	RedMissedBlocksLimit = 50
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
