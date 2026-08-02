package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anakin0xc06/validator-alert-bot/config"
	"github.com/anakin0xc06/validator-alert-bot/helpers"
	"github.com/jasonlvhit/gocron"
	tgbotapi "gopkg.in/telegram-bot-api.v4"

	"github.com/btcsuite/btcutil/bech32"
	"github.com/fatih/color"
)

var (
	subsMu      sync.RWMutex
	subscribers = make(map[string][]string)

	stateMu                sync.Mutex
	validatorsMissedBlocks = make(map[string]int64)
	validatorAlertLevels   = make(map[string]string)
	validatorJailed        = make(map[string]bool)
	slashingParamsCache    = make(map[string]slashingParamsEntry)

	// read-only after initBot
	networks         = make(map[string]map[string]string)
	validatorAliases = make(map[string]ValidatorAlias)
)

type ValidatorAlias struct {
	Network          string `json:"network"`
	Moniker          string `json:"moniker"`
	ValconsAddress   string `json:"valcons_address"`
	ValidatorAddress string `json:"validator_address"`
}

// BotState is the alert state persisted across restarts so the bot does not
// re-send jail/level alerts after a redeploy
type BotState struct {
	AlertLevels map[string]string `json:"alert_levels"`
	Jailed      map[string]bool   `json:"jailed"`
}

func loadJSONFile(path string, v interface{}, required bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if required {
			log.Fatalf("Failed to read %s: %v", path, err)
		}
		log.Printf("No %s found, starting fresh", path)
		return
	}
	if err := json.Unmarshal(data, v); err != nil {
		if required {
			log.Fatalf("Failed to parse %s: %v", path, err)
		}
		log.Printf("Failed to parse %s: %v", path, err)
	}
}

func saveJSONFile(path string, v interface{}) {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		log.Printf("Failed to marshal %s: %v", path, err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("Failed to write %s: %v", path, err)
	}
}

func initBot() {
	if err := os.MkdirAll(filepath.Dir(config.SubscribersFile), 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}
	loadJSONFile(config.NetworksFile, &networks, true)
	loadJSONFile(config.SubscribersFile, &subscribers, false)
	loadJSONFile(config.ValidatorsFile, &validatorsMissedBlocks, false)

	var state BotState
	loadJSONFile(config.StateFile, &state, false)
	if state.AlertLevels != nil {
		validatorAlertLevels = state.AlertLevels
	}
	if state.Jailed != nil {
		validatorJailed = state.Jailed
	}

	var aliases []ValidatorAlias
	loadJSONFile(config.ValidatorAliasesFile, &aliases, false)
	for _, alias := range aliases {
		validatorAliases[alias.ValconsAddress] = alias
	}
	loadUpgradeState()
	log.Printf("Loaded %d subscriber(s), %d network(s), %d alias(es)", len(subscribers), len(networks), len(validatorAliases))
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if config.BOT_API_KEY == "" {
		log.Fatal("BOT_API_KEY environment variable is not set")
	}
	bot, err := tgbotapi.NewBotAPI(config.BOT_API_KEY)
	if err != nil {
		log.Fatalf("Error in instantiating the bot: %v", err)
	}
	initBot()
	go SubscribersHandleScheduler(bot)
	go StartWebServer()
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates, err := bot.GetUpdatesChan(u)
	if err != nil {
		log.Fatalf("Error while receiving messages: %v", err)
	}
	color.Green("Started %s successfully", bot.Self.UserName)

	for update := range updates {
		if update.Message != nil && update.Message.IsCommand() {
			go func(update tgbotapi.Update) {
				runSafe("command handler", func() { MainHandler(bot, update) })
			}(update)
		}
	}
}

// runSafe runs fn and recovers from panics so one bad cycle or command
// cannot take the whole bot down
func runSafe(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in %s: %v", name, r)
		}
	}()
	fn()
}

const helpText = `*Cosmic Validator Alerts Bot*

Commands (work in groups as well as DMs):
/subscribe ` + "`<valcons addresses ...>`" + ` — subscribe to missed-block, uptime and jailing alerts. Alerts are always DM'd to you directly, so message me privately at least once or they won't arrive.
/unsubscribe — remove all your subscriptions
/uptime — signing window, missed blocks and uptime of your validators
/dashboard — uptime and safety for every validator configured in validator_aliases.json
/upgrades — list active chain-upgrade proposals (voting or passed), target heights and ETA
/help — show this help

Alerts: 🔴 missed blocks +100 in a check or window uptime < 80%, 🟡 uptime -1% or missed blocks +50-100 in a check, 🟢 recovering, 🚨 jailed / tombstoned. A 💚 health ping is sent every 6 hours. Chain upgrades: 🗳 proposal in voting, ⏰ upgrade incoming (1 day and 1-2 hours before, once passed), ✅ upgrade height reached, ⚠️ upgrade cancelled.`

// MainHandler ...
func MainHandler(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	if update.Message == nil || !update.Message.IsCommand() {
		return
	}
	command := update.Message.Command()
	log.Printf("Command /%s from user %d", command, helpers.GetUserID(update))

	// All commands work in both group chats and DMs. /subscribe is the one
	// exception worth calling out: alerts are always delivered by DM'ing
	// the subscribing user directly (sendTo uses their user ID as the chat
	// ID), which silently fails if that user has never messaged the bot
	// privately, regardless of which chat they ran /subscribe from.
	switch command {
	case "start", "help":
		helpers.SendMessage(bot, update, helpText, tgbotapi.ModeMarkdown)
	case "subscribe":
		HandleSubscribe(bot, update)
	case "unsubscribe":
		HandleUnsubscribe(bot, update)
	case "uptime":
		HandleUptime(bot, update)
	case "dashboard":
		HandleDashboard(bot, update)
	case "upgrades":
		HandleUpgradesCommand(bot, update)
	default:
		helpers.SendMessage(bot, update, "Unknown command, see /help", tgbotapi.ModeMarkdown)
	}
}

func HandleSubscribe(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	args := update.Message.CommandArguments()
	var validatorConsAddresses []string

	if len(args) > 0 {
		arguments := strings.Fields(args)
		for _, arg := range arguments {
			if isCorrectValConsAddress(arg) && !contains(validatorConsAddresses, arg) {
				validatorConsAddresses = append(validatorConsAddresses, arg)
			}
		}

		if len(validatorConsAddresses) > 0 {
			userId := helpers.GetUserID(update)
			subsMu.Lock()
			validators, ok := subscribers[fmt.Sprint(userId)]
			if !ok {
				subscribers[fmt.Sprint(userId)] = validatorConsAddresses
			} else {
				for _, val := range validatorConsAddresses {
					if !contains(validators, val) {
						validators = append(validators, val)
					}
				}
				subscribers[fmt.Sprint(userId)] = validators
			}
			saveJSONFile(config.SubscribersFile, subscribers)
			subsMu.Unlock()
			confirmation := "Subscribed to alerts. Use /uptime to see the current status."
			if !update.Message.Chat.IsPrivate() {
				confirmation += fmt.Sprintf("\n\n❗ Alerts are delivered by DM. If you haven't messaged me privately before, open https://t.me/%s and press Start, or alerts won't reach you.", bot.Self.UserName)
			}
			helpers.SendMessage(bot, update, confirmation, tgbotapi.ModeHTML)
			return
		} else {
			helpers.SendMessage(bot, update, "Invalid args: no valid valcons addresses found", tgbotapi.ModeHTML)
			return
		}

	} else {
		helpers.SendMessage(bot, update, "Invalid format, Please use /subscribe [validator consensus addresses ..]", tgbotapi.ModeHTML)
		return
	}
}

func HandleUnsubscribe(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	userId := helpers.GetUserID(update)
	subsMu.Lock()
	_, ok := subscribers[fmt.Sprint(userId)]
	if ok {
		delete(subscribers, fmt.Sprint(userId))
	}
	saveJSONFile(config.SubscribersFile, subscribers)
	subsMu.Unlock()
	text := "unsubscribed from alerts"
	helpers.SendMessage(bot, update, text, tgbotapi.ModeHTML)
}

// HandleUptime reports window size, missed blocks, uptime % and safety
// for every validator the user is subscribed to
func HandleUptime(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	userId := helpers.GetUserID(update)
	subsMu.RLock()
	validators := append([]string(nil), subscribers[fmt.Sprint(userId)]...)
	subsMu.RUnlock()

	if len(validators) == 0 {
		helpers.SendMessage(bot, update, "You have no subscriptions yet. Use /subscribe [validator consensus addresses ..] first.", tgbotapi.ModeMarkdown)
		return
	}

	var sb strings.Builder
	sb.WriteString("📊 *Validator Uptime*\n")
	for _, validator := range validators {
		name := displayName(validator)
		prefix := getPrefix(validator)
		if len(prefix) == 0 || len(networks[prefix]) == 0 {
			sb.WriteString(fmt.Sprintf("\n⚪️ %s\nNetwork not supported\n", name))
			continue
		}
		info, err := helpers.GetSigningInfo(networks[prefix]["rest"], validator)
		if err != nil {
			sb.WriteString(fmt.Sprintf("\n⚪️ %s\nFailed to fetch signing info\n", name))
			continue
		}
		missed, err := strconv.ParseInt(info.MissedBlocksCounter, 10, 64)
		if err != nil {
			sb.WriteString(fmt.Sprintf("\n⚪️ %s\nFailed to fetch signing info\n", name))
			continue
		}
		window := getSignedBlocksWindow(prefix)
		if window <= 0 {
			sb.WriteString(fmt.Sprintf("\n⚪️ %s\nMissing blocks: %d (slashing window unavailable)\n", name, missed))
			continue
		}
		uptime := float64(window-missed) * 100 / float64(window)
		jailed := info.Tombstoned || info.JailedUntil.After(time.Now().UTC())
		dot, status := "🟢", "SAFE"
		if missed*100 >= window*config.CriticalWindowPercent {
			dot, status = "🔴", "UNSAFE"
		}
		if jailed {
			dot, status = "🔴", "UNSAFE (jailed)"
		}
		sb.WriteString(fmt.Sprintf("\n%s %s\nWindow: %d blocks\nCurrently missing: %d\nUptime: %.2f%% — *%s*\n", dot, name, window, missed, uptime, status))
		if link := mintscanLink(validator); link != "" {
			sb.WriteString(fmt.Sprintf("🔗 [Check on Mintscan](%s)\n", link))
		}
	}
	helpers.SendMessage(bot, update, sb.String(), tgbotapi.ModeMarkdown)
}

// validatorSafetyStatus turns a signing-info reading into an uptime % and a
// safe/unsafe verdict based on the chain's real min_signed_per_window
// slashing param, rather than the flat config.CriticalWindowPercent used
// elsewhere. Falls back to config.CriticalWindowPercent when
// minSignedPerWindow is unavailable (e.g. the params fetch failed) so the
// dashboard still shows a meaningful verdict instead of always "safe".
func validatorSafetyStatus(missed, window int64, minSignedPerWindow float64, jailed bool) (uptimePct float64, safe bool) {
	uptimePct = float64(window-missed) * 100 / float64(window)
	threshold := 100 - float64(config.CriticalWindowPercent)
	if minSignedPerWindow > 0 {
		threshold = minSignedPerWindow * 100
	}
	safe = !jailed && uptimePct >= threshold
	return uptimePct, safe
}

// splitMessage breaks text into chunks no larger than limit, splitting only
// on line boundaries, so long dashboards stay under Telegram's 4096-char
// message cap without cutting a validator's entry in half.
func splitMessage(text string, limit int) []string {
	hadTrailingNewline := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	var chunks []string
	var cur strings.Builder
	for i, line := range lines {
		addition := line
		if i < len(lines)-1 || hadTrailingNewline {
			addition += "\n"
		}
		if cur.Len() > 0 && cur.Len()+len(addition) > limit {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		cur.WriteString(addition)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// HandleDashboard reports slashing-window uptime and safety for every
// validator configured in validator_aliases.json (not just the caller's own
// subscriptions), grouped by network. Safety is based on the chain's real
// min_signed_per_window rather than the flat threshold /uptime uses.
func HandleDashboard(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	if len(validatorAliases) == 0 {
		helpers.SendMessage(bot, update, "No validators configured in validator_aliases.json.", tgbotapi.ModeMarkdown)
		return
	}
	for _, chunk := range buildDashboardMessages() {
		helpers.SendMessage(bot, update, chunk, tgbotapi.ModeMarkdown)
	}
}

// dashboardRow is one validator's worth of data for the /dashboard command
// and the web dashboard. Note is set instead of the numeric/status fields
// when the row's data couldn't be fetched (e.g. "network not supported").
type dashboardRow struct {
	Network      string
	Moniker      string
	Missed       int64
	Window       int64
	UptimePct    float64
	Safe         bool
	Jailed       bool
	MintscanLink string
	Note         string
}

// collectDashboardRows fetches signing info for every validator configured
// in validator_aliases.json and returns one row per validator, sorted by
// (network, moniker). Shared by the /dashboard Telegram command and the web
// dashboard so both report from a single fetch/compute path.
func collectDashboardRows() []dashboardRow {
	type entry struct {
		address string
		alias   ValidatorAlias
	}
	entries := make([]entry, 0, len(validatorAliases))
	for address, alias := range validatorAliases {
		entries = append(entries, entry{address: address, alias: alias})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].alias.Network != entries[j].alias.Network {
			return entries[i].alias.Network < entries[j].alias.Network
		}
		return entries[i].alias.Moniker < entries[j].alias.Moniker
	})

	rows := make([]dashboardRow, 0, len(entries))
	for _, e := range entries {
		moniker := e.alias.Moniker
		if moniker == "" {
			moniker = e.address
		}
		row := dashboardRow{Network: e.alias.Network, Moniker: moniker, MintscanLink: mintscanLink(e.address)}

		prefix := getPrefix(e.address)
		if len(prefix) == 0 || len(networks[prefix]) == 0 {
			row.Note = "network not supported"
			rows = append(rows, row)
			continue
		}
		info, err := helpers.GetSigningInfo(networks[prefix]["rest"], e.address)
		if err != nil {
			row.Note = "failed to fetch signing info"
			rows = append(rows, row)
			continue
		}
		missed, err := strconv.ParseInt(info.MissedBlocksCounter, 10, 64)
		if err != nil {
			row.Note = "failed to fetch signing info"
			rows = append(rows, row)
			continue
		}
		params := getSlashingParamsCached(prefix)
		if params.Window <= 0 {
			row.Missed = missed
			row.Note = "slashing window unavailable"
			rows = append(rows, row)
			continue
		}
		jailed := info.Tombstoned || info.JailedUntil.After(time.Now().UTC())
		uptime, safe := validatorSafetyStatus(missed, params.Window, params.MinSignedPerWindow, jailed)
		row.Missed = missed
		row.Window = params.Window
		row.UptimePct = uptime
		row.Safe = safe
		row.Jailed = jailed
		rows = append(rows, row)
	}
	return rows
}

// buildDashboardMessages renders collectDashboardRows as Telegram markdown,
// split into Telegram-sized chunks.
func buildDashboardMessages() []string {
	var sb strings.Builder
	sb.WriteString("📊 *Validator Dashboard*\n")
	currentNetwork := ""
	for _, row := range collectDashboardRows() {
		if row.Network != currentNetwork {
			sb.WriteString(fmt.Sprintf("\n*%s*\n", row.Network))
			currentNetwork = row.Network
		}
		if row.Note != "" {
			sb.WriteString(fmt.Sprintf("⚪️ %s — %s\n", row.Moniker, row.Note))
			continue
		}
		dot, status := "🟢", "SAFE"
		if !row.Safe {
			dot, status = "🔴", "UNSAFE"
		}
		if row.Jailed {
			status += " (jailed)"
		}
		sb.WriteString(fmt.Sprintf("%s %s — missed %d/%d, uptime %.2f%% — *%s*\n", dot, row.Moniker, row.Missed, row.Window, row.UptimePct, status))
		if row.MintscanLink != "" {
			sb.WriteString(fmt.Sprintf("   🔗 [Mintscan](%s)\n", row.MintscanLink))
		}
	}

	return splitMessage(sb.String(), 3500)
}

func getPrefix(addr string) string {
	parts := strings.Split(addr, "val")
	if len(parts) > 1 {
		return parts[0]
	}
	return ""
}

func displayName(address string) string {
	if alias, ok := validatorAliases[address]; ok && alias.Moniker != "" {
		network := alias.Network
		if network == "" {
			network = getPrefix(address)
		}
		return fmt.Sprintf("%s (%s)", alias.Moniker, network)
	}
	if prefix := getPrefix(address); prefix != "" {
		return fmt.Sprintf("%s (%s)", address, prefix)
	}
	return address
}

// mintscanLink returns the explorer URL for a validator, or "" when the
// alias config has no network/valoper mapping for it
func mintscanLink(address string) string {
	alias, ok := validatorAliases[address]
	if !ok || alias.Network == "" || alias.ValidatorAddress == "" {
		return ""
	}
	return fmt.Sprintf("https://mintscan.io/%s/validators/%s", alias.Network, alias.ValidatorAddress)
}

// withMintscanLink appends an explorer link so subscribers can verify
// missed blocks directly on Mintscan
func withMintscanLink(text, validator string) string {
	if link := mintscanLink(validator); link != "" {
		return text + fmt.Sprintf("\n\n🔗 [Check on Mintscan](%s)", link)
	}
	return text
}

// slashingParamsEntry caches a network's slashing window size and the
// minimum fraction of it that must be signed to avoid being jailed
type slashingParamsEntry struct {
	Window             int64
	MinSignedPerWindow float64
}

func getSlashingParamsCached(prefix string) slashingParamsEntry {
	stateMu.Lock()
	entry, ok := slashingParamsCache[prefix]
	stateMu.Unlock()
	if ok {
		return entry
	}
	window, minSignedPerWindow, err := helpers.GetSlashingParams(networks[prefix]["rest"])
	if err != nil {
		log.Printf("Failed to fetch slashing params for %s: %v", prefix, err)
		return slashingParamsEntry{}
	}
	entry = slashingParamsEntry{Window: window, MinSignedPerWindow: minSignedPerWindow}
	stateMu.Lock()
	slashingParamsCache[prefix] = entry
	stateMu.Unlock()
	return entry
}

func getSignedBlocksWindow(prefix string) int64 {
	return getSlashingParamsCached(prefix).Window
}

// joinReasons turns a list of lowercase reason fragments (e.g. "uptime
// dropped 1.30 points since the last check") into a single capitalized,
// semicolon-separated sentence fragment
func joinReasons(reasons []string) string {
	s := strings.Join(reasons, "; ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// decideMissedBlocksAlert is the decision logic behind CheckValidator's
// missed-blocks alerting. Everything is evaluated fresh each check against
// only the current reading and the previous one — there is no persisted
// "how bad has this gotten historically" state — so a validator whose
// missed-blocks counter is merely staying high while trending down does not
// keep re-triggering the same alert; only an actual worsening this check
// (a missed-blocks jump, an uptime drop) or being critically low right now
// (window uptime under CriticalUptimePercent) fires anything.
//
//	Critical: missed blocks jumped by more than CriticalMissedBlocksDelta
//	          this check, OR window uptime is currently below CriticalUptimePercent.
//	Normal:   window uptime dropped by at least UptimeDropAlertPercent points
//	          since the last check, OR missed blocks increased by
//	          NormalMissedBlocksDelta..CriticalMissedBlocksDelta this check.
//	Recovery: previously alerting, and neither of the above holds anymore.
//
// It touches no global state so it can be tested without a network call.
func decideMissedBlocksAlert(name string, currentMissedBlocks, window int64, hasPrevious bool, previousMissedBlocks int64, level string) (alerts []string, newLevel string) {
	newLevel = level

	haveUptime := window > 0
	var uptimePct float64
	if haveUptime {
		uptimePct = float64(window-currentMissedBlocks) * 100 / float64(window)
	}

	var delta int64
	if hasPrevious {
		delta = currentMissedBlocks - previousMissedBlocks
	}

	var criticalReasons, normalReasons []string
	if hasPrevious && delta > config.CriticalMissedBlocksDelta {
		criticalReasons = append(criticalReasons, fmt.Sprintf("missed blocks increased by %d in this check", delta))
	}
	if haveUptime && uptimePct < config.CriticalUptimePercent {
		criticalReasons = append(criticalReasons, fmt.Sprintf("window uptime is below %.0f%%", config.CriticalUptimePercent))
	}
	if hasPrevious && haveUptime {
		previousUptime := float64(window-previousMissedBlocks) * 100 / float64(window)
		if drop := previousUptime - uptimePct; drop >= config.UptimeDropAlertPercent {
			normalReasons = append(normalReasons, fmt.Sprintf("uptime dropped %.2f points since the last check", drop))
		}
	}
	if hasPrevious && delta >= config.NormalMissedBlocksDelta && delta <= config.CriticalMissedBlocksDelta {
		normalReasons = append(normalReasons, fmt.Sprintf("missed blocks increased by %d in this check", delta))
	}

	var uptimeLine, deltaSuffix string
	if haveUptime {
		uptimeLine = fmt.Sprintf("\nUptime: *%.2f%%*", uptimePct)
	}
	if hasPrevious {
		deltaSuffix = fmt.Sprintf(" (%+d this check)", delta)
	}

	switch {
	case len(criticalReasons) > 0:
		alerts = append(alerts, fmt.Sprintf("🔴 *CRITICAL: Missing Blocks*\n\n%s\nMissed blocks: *%d*%s%s\n\n%s. Please check the node immediately.",
			name, currentMissedBlocks, deltaSuffix, uptimeLine, joinReasons(criticalReasons)))
		newLevel = "critical"

	case len(normalReasons) > 0:
		alerts = append(alerts, fmt.Sprintf("🟡 *Missed Blocks Alert*\n\n%s\nMissed blocks: *%d*%s%s\n\n%s.",
			name, currentMissedBlocks, deltaSuffix, uptimeLine, joinReasons(normalReasons)))
		newLevel = "normal"

	case level != "":
		alerts = append(alerts, fmt.Sprintf("🟢 *Recovering*\n\n%s\nMissed blocks: *%d*%s%s",
			name, currentMissedBlocks, deltaSuffix, uptimeLine))
		newLevel = ""
	}

	return alerts, newLevel
}

// CheckValidator inspects a validator's signing info and returns the alerts to send
func CheckValidator(validator string) []string {
	var alerts []string
	prefix := getPrefix(validator)
	if len(prefix) == 0 || len(networks[prefix]) == 0 {
		return nil
	}
	info, err := helpers.GetSigningInfo(networks[prefix]["rest"], validator)
	if err != nil {
		log.Printf("Error fetching signing info for %s: %v", validator, err)
		return nil
	}
	currentMissedBlocks, err := strconv.ParseInt(info.MissedBlocksCounter, 10, 64)
	if err != nil {
		log.Printf("Bad missed blocks counter for %s: %v", validator, err)
		return nil
	}
	name := displayName(validator)
	window := getSignedBlocksWindow(prefix)
	log.Printf("%s missed blocks: %d", validator, currentMissedBlocks)

	stateMu.Lock()
	defer stateMu.Unlock()

	jailedNow := info.Tombstoned || info.JailedUntil.After(time.Now().UTC())
	if jailedNow && !validatorJailed[validator] {
		if info.Tombstoned {
			alerts = append(alerts, fmt.Sprintf("🚨 *CRITICAL: Validator Tombstoned*\n\n%s has been tombstoned (permanently jailed for double signing).", name))
		} else {
			alerts = append(alerts, fmt.Sprintf("🚨 *Validator Jailed*\n\n%s has been jailed until %s.", name, info.JailedUntil.Format(time.RFC1123)))
		}
	}
	if !jailedNow && validatorJailed[validator] {
		alerts = append(alerts, fmt.Sprintf("✅ *Validator Unjailed*\n\n%s is out of jail and can rejoin the active set.", name))
	}
	validatorJailed[validator] = jailedNow

	previousMissedBlocks, hasPrevious := validatorsMissedBlocks[validator]
	level := validatorAlertLevels[validator]

	missedAlerts, newLevel := decideMissedBlocksAlert(name, currentMissedBlocks, window, hasPrevious, previousMissedBlocks, level)
	alerts = append(alerts, missedAlerts...)
	validatorAlertLevels[validator] = newLevel
	validatorsMissedBlocks[validator] = currentMissedBlocks
	for i := range alerts {
		alerts[i] = withMintscanLink(alerts[i], validator)
	}
	return alerts
}

func copySubscribers() map[string][]string {
	subsMu.RLock()
	defer subsMu.RUnlock()
	subsCopy := make(map[string][]string, len(subscribers))
	for user, validators := range subscribers {
		subsCopy[user] = append([]string(nil), validators...)
	}
	return subsCopy
}

func sendTo(bot *tgbotapi.BotAPI, userId int64, text string) {
	msg := tgbotapi.NewMessage(userId, text)
	msg.ParseMode = tgbotapi.ModeMarkdown
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Failed to send message to %d: %v", userId, err)
	}
}

func HandleSubscribers(bot *tgbotapi.BotAPI) {
	log.Println("Checking missed blocks ...")
	subsCopy := copySubscribers()
	alertsByValidator := make(map[string][]string)
	for _, validators := range subsCopy {
		for _, validator := range validators {
			if _, checked := alertsByValidator[validator]; !checked {
				alertsByValidator[validator] = CheckValidator(validator)
			}
		}
	}
	for user, validators := range subsCopy {
		userId, err := strconv.ParseInt(user, 10, 64)
		if err != nil {
			continue
		}
		for _, validator := range validators {
			for _, text := range alertsByValidator[validator] {
				log.Println(text)
				sendTo(bot, userId, text)
			}
		}
	}
	saveValidatorState()
	log.Println("Updated validators missed blocks data")
}

func saveValidatorState() {
	stateMu.Lock()
	defer stateMu.Unlock()
	saveJSONFile(config.ValidatorsFile, validatorsMissedBlocks)
	saveJSONFile(config.StateFile, BotState{
		AlertLevels: validatorAlertLevels,
		Jailed:      validatorJailed,
	})
}

// SendHealthCheck tells every subscriber the bot is alive and summarizes their validators
func SendHealthCheck(bot *tgbotapi.BotAPI) {
	log.Println("Sending health check ...")
	subsCopy := copySubscribers()
	for user, validators := range subsCopy {
		userId, err := strconv.ParseInt(user, 10, 64)
		if err != nil {
			continue
		}
		var sb strings.Builder
		sb.WriteString("💚 *Bot Health Check*\n\nBot is active and monitoring your validators:\n")
		stateMu.Lock()
		for _, validator := range validators {
			state := "✅ signing"
			if validatorJailed[validator] {
				state = "⛔️ jailed"
			} else if level := validatorAlertLevels[validator]; level != "" {
				state = "⚠️ missing blocks (" + level + ")"
			}
			sb.WriteString(fmt.Sprintf("\n%s\nmissed blocks: %d — %s\n", displayName(validator), validatorsMissedBlocks[validator], state))
			if link := mintscanLink(validator); link != "" {
				sb.WriteString(fmt.Sprintf("🔗 [Check on Mintscan](%s)\n", link))
			}
		}
		stateMu.Unlock()
		sb.WriteString(fmt.Sprintf("\nMissed-block checks run every %d minutes, next health ping in %d hours.", config.CheckIntervalSeconds/60, config.HealthCheckIntervalHours))
		sendTo(bot, userId, sb.String())
	}
}

func isCorrectValConsAddress(address string) bool {
	hrp, _, err := bech32.Decode(address)
	if err != nil {
		log.Printf("Invalid bech32 address %q: %v", address, err)
		return false
	}
	if strings.Contains(hrp, "valcons") {
		return true
	}
	return false
}

func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}

func SubscribersHandleScheduler(bot *tgbotapi.BotAPI) {
	runSafe("missed blocks check", func() { HandleSubscribers(bot) })
	runSafe("health check", func() { SendHealthCheck(bot) })
	runSafe("upgrade check", func() { HandleUpgrades(bot) })
	s := gocron.NewScheduler()
	log.Println("Starting blocks monitoring scheduler ...")
	s.Every(config.CheckIntervalSeconds).Seconds().Do(runSafe, "missed blocks check", func() { HandleSubscribers(bot) })
	s.Every(config.HealthCheckIntervalHours).Hours().Do(runSafe, "health check", func() { SendHealthCheck(bot) })
	s.Every(config.UpgradeCheckIntervalSeconds).Seconds().Do(runSafe, "upgrade check", func() { HandleUpgrades(bot) })
	<-s.Start()
}
