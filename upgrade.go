package main

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anakin0xc06/validator-alert-bot/config"
	"github.com/anakin0xc06/validator-alert-bot/helpers"
	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

// TrackedUpgrade is a scheduled chain upgrade the bot is watching, persisted
// across restarts so alerts are not re-sent
type TrackedUpgrade struct {
	Network     string `json:"network"`
	ProposalID  string `json:"proposal_id"`
	Name        string `json:"name"`
	Height      int64  `json:"height"`
	Info        string `json:"info,omitempty"`
	AlertedDay  bool   `json:"alerted_day"`
	AlertedHour bool   `json:"alerted_hour"`
}

// BlockSample is the most recently observed (height, time) pair for a
// network, used to self-measure the average block time between check cycles
type BlockSample struct {
	Height int64     `json:"height"`
	Time   time.Time `json:"time"`
}

// UpgradeState is the upgrade-watcher state persisted to config.UpgradesFile
type UpgradeState struct {
	Upgrades      map[string]*TrackedUpgrade `json:"upgrades"`
	BlockSamples  map[string]BlockSample     `json:"block_samples"`
	AvgBlockTimes map[string]int64           `json:"avg_block_times_ns"`
}

var (
	upgradeMu sync.Mutex
	// trackedUpgrades keyed by "<network>:<proposalID>" (or "<network>:plan:<name>"
	// when only discovered via the upgrade module's current_plan query)
	trackedUpgrades = make(map[string]*TrackedUpgrade)
	blockSamples    = make(map[string]BlockSample)
	avgBlockTimes   = make(map[string]time.Duration)
)

func upgradeKey(network string, plan helpers.ChainUpgradePlan) string {
	if plan.ProposalID != "" {
		return network + ":" + plan.ProposalID
	}
	return network + ":plan:" + plan.Name
}

func loadUpgradeState() {
	var state UpgradeState
	loadJSONFile(config.UpgradesFile, &state, false)
	if state.Upgrades != nil {
		trackedUpgrades = state.Upgrades
	}
	if state.BlockSamples != nil {
		blockSamples = state.BlockSamples
	}
	for prefix, ns := range state.AvgBlockTimes {
		avgBlockTimes[prefix] = time.Duration(ns)
	}
}

func saveUpgradeState() {
	upgradeMu.Lock()
	defer upgradeMu.Unlock()
	avgNs := make(map[string]int64, len(avgBlockTimes))
	for prefix, d := range avgBlockTimes {
		avgNs[prefix] = int64(d)
	}
	saveJSONFile(config.UpgradesFile, UpgradeState{Upgrades: trackedUpgrades, BlockSamples: blockSamples, AvgBlockTimes: avgNs})
}

// CheckUpgrades polls every configured network for scheduled software
// upgrades and returns the alerts to send, keyed by network prefix
func CheckUpgrades() map[string][]string {
	alertsByNetwork := make(map[string][]string)
	for prefix, endpoints := range networks {
		alerts := checkNetworkUpgrade(prefix, endpoints["rest"], endpoints["rpc"])
		if len(alerts) > 0 {
			alertsByNetwork[prefix] = alerts
		}
	}
	return alertsByNetwork
}

// checkNetworkUpgrade refreshes the block-time estimate for a network,
// discovers any newly-passed software-upgrade proposals, and returns alerts
// for upgrades that just crossed a warning threshold or the target height
func checkNetworkUpgrade(prefix, rest, rpc string) []string {
	if rest == "" || rpc == "" {
		return nil
	}

	height, blockTime, err := helpers.GetLatestBlock(rpc)
	if err != nil {
		log.Printf("Failed to get latest block for %s: %v", prefix, err)
		return nil
	}

	upgradeMu.Lock()
	prevSample, hasPrevSample := blockSamples[prefix]
	blockSamples[prefix] = BlockSample{Height: height, Time: blockTime}
	if hasPrevSample && height > prevSample.Height {
		avgBlockTimes[prefix] = blockTime.Sub(prevSample.Time) / time.Duration(height-prevSample.Height)
	}
	avgBlockTime := avgBlockTimes[prefix]
	upgradeMu.Unlock()

	plans, cancelled, err := helpers.GetPassedSoftwareUpgrades(rest)
	if err != nil {
		log.Printf("Failed to get gov proposals for %s: %v", prefix, err)
	}
	// current_plan is the upgrade module's own authoritative record; used as
	// a fallback in case proposal parsing/pagination missed something
	if currentPlan, cpErr := helpers.GetCurrentUpgradePlan(rest); cpErr != nil {
		log.Printf("Failed to get current upgrade plan for %s: %v", prefix, cpErr)
	} else if currentPlan != nil {
		matched := false
		for _, p := range plans {
			if p.Height == currentPlan.Height {
				matched = true
				break
			}
		}
		if !matched {
			plans = append(plans, *currentPlan)
		}
	}

	var alerts []string
	upgradeMu.Lock()
	defer upgradeMu.Unlock()

	if cancelled {
		for key, tracked := range trackedUpgrades {
			if tracked.Network != prefix {
				continue
			}
			alerts = append(alerts, fmt.Sprintf("⚠️ *Upgrade Cancelled*\n\nNetwork: *%s*\nThe scheduled upgrade *%s* (target height %d) appears to have been cancelled via governance.", strings.ToUpper(prefix), tracked.Name, tracked.Height))
			delete(trackedUpgrades, key)
		}
	}

	for _, plan := range plans {
		if plan.Height <= height {
			continue // already at/past this height, nothing to track
		}
		key := upgradeKey(prefix, plan)
		if tracked, exists := trackedUpgrades[key]; exists {
			tracked.Height = plan.Height
			tracked.Name = plan.Name
			if plan.ProposalID != "" {
				tracked.ProposalID = plan.ProposalID
			}
			continue
		}
		trackedUpgrades[key] = &TrackedUpgrade{
			Network:    prefix,
			ProposalID: plan.ProposalID,
			Name:       plan.Name,
			Height:     plan.Height,
			Info:       plan.Info,
		}
		log.Printf("Tracking new upgrade on %s: %s at height %d (proposal #%s)", prefix, plan.Name, plan.Height, plan.ProposalID)
	}

	for key, tracked := range trackedUpgrades {
		if tracked.Network != prefix {
			continue
		}
		if height >= tracked.Height {
			alerts = append(alerts, fmt.Sprintf("✅ *Upgrade Height Reached*\n\nNetwork: *%s*\nUpgrade *%s* (target height %d) has been reached at current height %d. Make sure your validator node has upgraded.", strings.ToUpper(prefix), tracked.Name, tracked.Height, height))
			delete(trackedUpgrades, key)
			continue
		}
		if avgBlockTime <= 0 {
			continue // no block-time estimate yet, wait for the next sample
		}
		eta := time.Duration(tracked.Height-height) * avgBlockTime
		if !tracked.AlertedDay && eta <= config.UpgradeDayWarningHours*time.Hour {
			tracked.AlertedDay = true
			alerts = append(alerts, upgradeAlertText(prefix, tracked, eta, height))
		}
		if !tracked.AlertedHour && eta <= config.UpgradeHourWarningHours*time.Hour {
			tracked.AlertedHour = true
			alerts = append(alerts, upgradeAlertText(prefix, tracked, eta, height))
		}
	}

	return alerts
}

func upgradeAlertText(prefix string, u *TrackedUpgrade, eta time.Duration, currentHeight int64) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⏰ *Upgrade Incoming: %s*\n\n", strings.ToUpper(prefix)))
	sb.WriteString(fmt.Sprintf("Upgrade: *%s*\n", u.Name))
	if u.ProposalID != "" {
		sb.WriteString(fmt.Sprintf("Proposal: #%s\n", u.ProposalID))
	}
	sb.WriteString(fmt.Sprintf("Target height: *%d* (current: %d)\n", u.Height, currentHeight))
	sb.WriteString(fmt.Sprintf("Estimated time: ~%s (around %s)\n", formatDuration(eta), time.Now().UTC().Add(eta).Format(time.RFC1123)))
	sb.WriteString("\nMake sure your validator node is upgraded before the target height.")
	return sb.String()
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// HandleUpgrades is the scheduled job: checks every network for upgrade
// warnings, notifies subscribers who have a validator on the affected
// network, and persists state
func HandleUpgrades(bot *tgbotapi.BotAPI) {
	log.Println("Checking for scheduled upgrades ...")
	alertsByNetwork := CheckUpgrades()
	if len(alertsByNetwork) > 0 {
		subsCopy := copySubscribers()
		for user, validators := range subsCopy {
			userId, err := strconv.ParseInt(user, 10, 64)
			if err != nil {
				continue
			}
			userNetworks := make(map[string]bool)
			for _, validator := range validators {
				if prefix := getPrefix(validator); prefix != "" {
					userNetworks[prefix] = true
				}
			}
			for prefix := range userNetworks {
				for _, text := range alertsByNetwork[prefix] {
					log.Println(text)
					sendTo(bot, userId, text)
				}
			}
		}
	}
	saveUpgradeState()
	log.Println("Updated upgrade watcher state")
}

// HandleUpgradesCommand handles /upgrades: lists full details for every
// currently tracked upgrade (saved from governance proposals) across all
// networks, including the live ETA. Informational, not gated by
// subscription, and works in both group chats and DMs.
func HandleUpgradesCommand(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	upgradeMu.Lock()
	pending := make([]*TrackedUpgrade, 0, len(trackedUpgrades))
	for _, u := range trackedUpgrades {
		pending = append(pending, u)
	}
	upgradeMu.Unlock()

	if len(pending) == 0 {
		helpers.SendMessage(bot, update, "No scheduled upgrades are currently being tracked.", tgbotapi.ModeMarkdown)
		return
	}

	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Network != pending[j].Network {
			return pending[i].Network < pending[j].Network
		}
		return pending[i].Height < pending[j].Height
	})

	var sb strings.Builder
	sb.WriteString("🛠 *Active Upgrades*\n")
	for _, u := range pending {
		sb.WriteString(fmt.Sprintf("\n*%s* — %s\n", strings.ToUpper(u.Network), u.Name))
		if u.ProposalID != "" {
			sb.WriteString(fmt.Sprintf("Proposal: #%s\n", u.ProposalID))
		}
		sb.WriteString(fmt.Sprintf("Target height: *%d*\n", u.Height))

		upgradeMu.Lock()
		sample, hasSample := blockSamples[u.Network]
		avg := avgBlockTimes[u.Network]
		upgradeMu.Unlock()

		if hasSample {
			remaining := u.Height - sample.Height
			sb.WriteString(fmt.Sprintf("Current height: %d (%d blocks remaining)\n", sample.Height, remaining))
			if avg > 0 {
				eta := time.Duration(remaining) * avg
				sb.WriteString(fmt.Sprintf("Estimated time: ~%s (around %s)\n", formatDuration(eta), time.Now().UTC().Add(eta).Format(time.RFC1123)))
			}
		}
	}
	helpers.SendMessage(bot, update, sb.String(), tgbotapi.ModeMarkdown)
}
