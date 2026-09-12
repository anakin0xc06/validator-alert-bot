package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anakin0xc06/validator-alert-bot/config"
	"github.com/anakin0xc06/validator-alert-bot/helpers"
	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

// govStatusVotingPeriod/govStatusPassed are the proposal status strings the
// gov REST APIs report while a proposal is being voted on and once it has
// passed, respectively
const (
	govStatusVotingPeriod = "PROPOSAL_STATUS_VOTING_PERIOD"
	govStatusPassed       = "PROPOSAL_STATUS_PASSED"
)

// TrackedUpgrade is a scheduled chain upgrade the bot is watching, persisted
// across restarts so alerts are not re-sent. Upgrades whose proposal is
// still in the voting period are tracked (and shown in /upgrades), and an
// early heads-up alert fires once when a proposal first enters voting.
// Height-based alerts (ETA warnings, height reached) only ever fire once
// Status reaches govStatusPassed, since a plan isn't confirmed until then.
type TrackedUpgrade struct {
	Network       string    `json:"network"`
	ProposalID    string    `json:"proposal_id"`
	Name          string    `json:"name"`
	Height        int64     `json:"height"`
	Info          string    `json:"info,omitempty"`
	Status        string    `json:"status"`
	VotingEndTime time.Time `json:"voting_end_time,omitempty"`
	AlertedVoting bool      `json:"alerted_voting"`
	AlertedDay    bool      `json:"alerted_day"`
	AlertedHour   bool      `json:"alerted_hour"`
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

	// covers both voting-period and passed software-upgrade proposals, so
	// operators get an early heads-up before a vote even concludes
	plans, cancelled, err := helpers.GetSoftwareUpgradeProposals(rest)
	if err != nil {
		log.Printf("Failed to get gov proposals for %s: %v", prefix, err)
	}
	// current_plan is the upgrade module's own authoritative record of a
	// confirmed (passed) plan; used as a fallback in case proposal
	// parsing/pagination missed something
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

	discovered := make(map[string]bool, len(plans))
	for _, plan := range plans {
		if plan.Height <= height {
			continue // already at/past this height, nothing to track
		}
		key := upgradeKey(prefix, plan)
		discovered[key] = true
		tracked, exists := trackedUpgrades[key]
		if !exists {
			tracked = &TrackedUpgrade{Network: prefix}
			trackedUpgrades[key] = tracked
			log.Printf("Tracking new upgrade on %s: %s at height %d (proposal #%s, status %s)", prefix, plan.Name, plan.Height, plan.ProposalID, plan.Status)
		}
		tracked.Height = plan.Height
		tracked.Name = plan.Name
		tracked.Info = plan.Info
		tracked.Status = plan.Status
		tracked.VotingEndTime = plan.VotingEndTime
		if plan.ProposalID != "" {
			tracked.ProposalID = plan.ProposalID
		}
		// early heads-up the moment a proposal enters voting, fired once;
		// a proposal first discovered already-passed never gets this alert,
		// since it was never actually seen in voting
		if tracked.Status == govStatusVotingPeriod && !tracked.AlertedVoting {
			tracked.AlertedVoting = true
			alerts = append(alerts, votingAlertText(prefix, tracked))
		}
	}

	for key, tracked := range trackedUpgrades {
		if tracked.Network != prefix {
			continue
		}
		// a still-voting proposal that dropped out of discovery was either
		// rejected/failed or expired. If we alerted when it entered voting,
		// tell subscribers it's resolved instead of just going silent;
		// otherwise (e.g. it never lived long enough to be seen) drop it
		// quietly, same as before. Passed upgrades are never pruned this way
		// (they persist purely on height, in case they fall off pagination).
		if tracked.Status != govStatusPassed && !discovered[key] {
			if tracked.AlertedVoting {
				alerts = append(alerts, rejectedAlertText(prefix, tracked))
			}
			log.Printf("Dropping no-longer-active upgrade proposal on %s: %s (proposal #%s)", prefix, tracked.Name, tracked.ProposalID)
			delete(trackedUpgrades, key)
			continue
		}
		if height >= tracked.Height {
			if tracked.Status == govStatusPassed {
				alerts = append(alerts, fmt.Sprintf("✅ *Upgrade Height Reached*\n\nNetwork: *%s*\nUpgrade *%s* (target height %d) has been reached at current height %d. Make sure your validator node has upgraded.", strings.ToUpper(prefix), tracked.Name, tracked.Height, height))
			}
			delete(trackedUpgrades, key)
			continue
		}
		if tracked.Status != govStatusPassed {
			continue // still just a proposal, don't push ETA alerts until it's confirmed
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

func votingAlertText(prefix string, u *TrackedUpgrade) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🗳 *Upgrade Proposal In Voting: %s*\n\n", strings.ToUpper(prefix)))
	sb.WriteString(fmt.Sprintf("Upgrade: *%s*\n", u.Name))
	if u.ProposalID != "" {
		sb.WriteString(fmt.Sprintf("Proposal: #%s\n", u.ProposalID))
	}
	sb.WriteString(fmt.Sprintf("Target height: *%d*\n", u.Height))
	if !u.VotingEndTime.IsZero() {
		sb.WriteString(fmt.Sprintf("Voting ends: %s\n", u.VotingEndTime.UTC().Format(time.RFC1123)))
	}
	sb.WriteString("\nNot yet confirmed — the proposal could still be rejected or fail to reach quorum.")
	return sb.String()
}

func rejectedAlertText(prefix string, u *TrackedUpgrade) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("❌ *Upgrade Proposal Rejected/Expired: %s*\n\n", strings.ToUpper(prefix)))
	sb.WriteString(fmt.Sprintf("Upgrade: *%s*\n", u.Name))
	if u.ProposalID != "" {
		sb.WriteString(fmt.Sprintf("Proposal: #%s\n", u.ProposalID))
	}
	sb.WriteString("\nThis proposal did not pass and is no longer being tracked.")
	return sb.String()
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

// HandleUpgrades is the scheduled job: checks every network that has at
// least one subscribed validator for upgrade warnings, posts them to
// config.UpgradesChatID, and persists state
func HandleUpgrades(bot *tgbotapi.BotAPI) {
	log.Println("Checking for scheduled upgrades ...")
	alertsByNetwork := CheckUpgrades()
	if len(alertsByNetwork) > 0 {
		subsCopy := copySubscribers()
		subscribedNetworks := make(map[string]bool)
		for _, validators := range subsCopy {
			for _, validator := range validators {
				if prefix := getPrefix(validator); prefix != "" {
					subscribedNetworks[prefix] = true
				}
			}
		}
		for prefix := range subscribedNetworks {
			for _, text := range alertsByNetwork[prefix] {
				log.Println(text)
				sendToChat(bot, config.UpgradesChatID, text)
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
		icon := "🗳"
		if u.Status == govStatusPassed {
			icon = "✅"
		}
		sb.WriteString(fmt.Sprintf("\n%s *%s* — %s\n", icon, strings.ToUpper(u.Network), u.Name))
		if u.ProposalID != "" {
			sb.WriteString(fmt.Sprintf("Proposal: #%s\n", u.ProposalID))
		}
		if u.Status != govStatusPassed {
			sb.WriteString("Status: in voting, not yet confirmed\n")
			if !u.VotingEndTime.IsZero() {
				sb.WriteString(fmt.Sprintf("Voting ends: %s\n", u.VotingEndTime.UTC().Format(time.RFC1123)))
			}
		}
		sb.WriteString(fmt.Sprintf("Target height: *%d*\n", u.Height))

		upgradeMu.Lock()
		sample, hasSample := blockSamples[u.Network]
		avg := avgBlockTimes[u.Network]
		upgradeMu.Unlock()

		if hasSample {
			remaining := u.Height - sample.Height
			sb.WriteString(fmt.Sprintf("Current height: %d (%d blocks remaining)\n", sample.Height, remaining))
			if avg > 0 && u.Status == govStatusPassed {
				eta := time.Duration(remaining) * avg
				sb.WriteString(fmt.Sprintf("Estimated time: ~%s (around %s)\n", formatDuration(eta), time.Now().UTC().Add(eta).Format(time.RFC1123)))
			}
		}
	}
	helpers.SendMessage(bot, update, sb.String(), tgbotapi.ModeMarkdown)
}
