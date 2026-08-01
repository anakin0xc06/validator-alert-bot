package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	signedBlocksWindows    = make(map[string]int64)

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

Commands:
/subscribe ` + "`<valcons addresses ...>`" + ` — subscribe to missed-block, jailing and slashing-risk alerts
/unsubscribe — remove all your subscriptions
/uptime — signing window, missed blocks and uptime of your validators
/upgrades — list active chain-upgrade proposals (voting or passed), target heights and ETA (works in groups too)
/help — show this help

Alerts: 🟡 missing blocks, 🔴 missing a lot of blocks, 🟢 recovering, 🚨 jailed / slashing risk. A 💚 health ping is sent every 6 hours. Chain upgrades: 🗳 proposal in voting, ⏰ upgrade incoming (1 day and 1-2 hours before, once passed), ✅ upgrade height reached, ⚠️ upgrade cancelled.`

// MainHandler ...
func MainHandler(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	if update.Message == nil || !update.Message.IsCommand() {
		return
	}
	command := update.Message.Command()
	log.Printf("Command /%s from user %d", command, helpers.GetUserID(update))

	// /upgrades is read-only and has no per-user state, so unlike the
	// subscription commands below it also works in group chats
	if command == "upgrades" {
		HandleUpgradesCommand(bot, update)
		return
	}

	if !update.Message.Chat.IsPrivate() {
		return
	}

	switch command {
	case "start", "help":
		helpers.SendMessage(bot, update, helpText, tgbotapi.ModeMarkdown)
	case "subscribe":
		HandleSubscribe(bot, update)
	case "unsubscribe":
		HandleUnsubscribe(bot, update)
	case "uptime":
		HandleUptime(bot, update)
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
			helpers.SendMessage(bot, update, "Subscribed to alerts. Use /uptime to see the current status.", tgbotapi.ModeHTML)
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

func getSignedBlocksWindow(prefix string) int64 {
	stateMu.Lock()
	window, ok := signedBlocksWindows[prefix]
	stateMu.Unlock()
	if ok {
		return window
	}
	window, err := helpers.GetSignedBlocksWindow(networks[prefix]["rest"])
	if err != nil {
		log.Printf("Failed to fetch slashing params for %s: %v", prefix, err)
		return 0
	}
	stateMu.Lock()
	signedBlocksWindows[prefix] = window
	stateMu.Unlock()
	return window
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
	delta := currentMissedBlocks - previousMissedBlocks
	level := validatorAlertLevels[validator]
	interval := config.CheckIntervalSeconds / 60

	switch {
	case window > 0 && currentMissedBlocks*100 >= window*config.CriticalWindowPercent:
		if level != "critical" || currentMissedBlocks > previousMissedBlocks {
			percent := float64(currentMissedBlocks) * 100 / float64(window)
			alerts = append(alerts, fmt.Sprintf("🚨 *CRITICAL: Slashing Risk*\n\n%s is missing *%d* of the last *%d* blocks (%.1f%% of the slashing window) and is at risk of being jailed and slashed.", name, currentMissedBlocks, window, percent))
		}
		validatorAlertLevels[validator] = "critical"
	case hasPrevious && delta >= config.RedMissedBlocksLimit:
		alerts = append(alerts, fmt.Sprintf("🔴 *High Missed Blocks Alert*\n\n%s is missing a lot of blocks: *%d → %d* (+%d in the last %d minutes). Please check the node immediately.", name, previousMissedBlocks, currentMissedBlocks, delta, interval))
		validatorAlertLevels[validator] = "red"
	case hasPrevious && delta >= config.MissedBlocksLimit:
		alerts = append(alerts, fmt.Sprintf("🟡 *Missed Blocks Alert*\n\n%s is missing blocks: *%d → %d* (+%d in the last %d minutes).", name, previousMissedBlocks, currentMissedBlocks, delta, interval))
		validatorAlertLevels[validator] = "yellow"
	case hasPrevious && currentMissedBlocks < previousMissedBlocks && level != "":
		alerts = append(alerts, fmt.Sprintf("🟢 *Recovering*\n\n%s is signing again, missed blocks decreasing: *%d → %d*.", name, previousMissedBlocks, currentMissedBlocks))
		validatorAlertLevels[validator] = ""
	}

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
	saveJSONFile(config.StateFile, BotState{AlertLevels: validatorAlertLevels, Jailed: validatorJailed})
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
