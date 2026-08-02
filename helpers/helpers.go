package helpers

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

// userAgentTransport sets a descriptive User-Agent on every outgoing
// request. Some public RPC/REST providers deprioritize or throttle the
// default Go-http-client UA, so this is cheap insurance against that.
type userAgentTransport struct {
	base      http.RoundTripper
	userAgent string
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", t.userAgent)
	return t.base.RoundTrip(req)
}

// httpClient is a dedicated client with a timeout so a stuck REST endpoint
// cannot hang a whole check cycle
var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &userAgentTransport{
		base: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		userAgent: "validator-alert-bot/1.0 (+https://github.com/anakin0xc06/validator-alert-bot)",
	},
}

type MissedBlocksResponse struct {
	ValSigningInfo ValSigningInfo `json:"val_signing_info"`
}
type ValSigningInfo struct {
	Address             string    `json:"address"`
	StartHeight         string    `json:"start_height"`
	IndexOffset         string    `json:"index_offset"`
	JailedUntil         time.Time `json:"jailed_until"`
	Tombstoned          bool      `json:"tombstoned"`
	MissedBlocksCounter string    `json:"missed_blocks_counter"`
}

type SlashingParamsResponse struct {
	Params SlashingParams `json:"params"`
}
type SlashingParams struct {
	SignedBlocksWindow string `json:"signed_blocks_window"`
	MinSignedPerWindow string `json:"min_signed_per_window"`
}

// GetUserName ...
func GetUserName(u tgbotapi.Update) string {
	var username string
	if u.CallbackQuery != nil {
		username = u.CallbackQuery.From.UserName
	}
	if u.Message != nil {
		username = u.Message.From.UserName
	}
	return username
}

// GetChatID ...
func GetChatID(u tgbotapi.Update) int64 {
	var chatID int64
	if u.CallbackQuery != nil {
		chatID = u.CallbackQuery.Message.Chat.ID
	}
	if u.Message != nil {
		chatID = u.Message.Chat.ID
	}
	return chatID
}

// GetUserID ...
func GetUserID(u tgbotapi.Update) int {
	var userID int
	if u.CallbackQuery != nil {
		userID = u.CallbackQuery.From.ID
	}
	if u.Message != nil {
		userID = u.Message.From.ID
	}
	return userID
}

// GetMsgID ...
func GetMsgID(u tgbotapi.Update) int {
	var MsgID int
	if u.CallbackQuery != nil {
		MsgID = u.CallbackQuery.Message.MessageID
	}
	if u.Message != nil {
		MsgID = u.Message.MessageID
	}
	return MsgID
}

func send(bot *tgbotapi.BotAPI, msg tgbotapi.Chattable) {
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Failed to send message: %v", err)
	}
}

// SendMessage ...
func SendMessage(bot *tgbotapi.BotAPI, update tgbotapi.Update, text string,
	mode string, btns ...tgbotapi.InlineKeyboardMarkup) {

	if update.Message != nil {
		msg := tgbotapi.NewMessage(GetChatID(update), text)
		if len(btns) > 0 {
			msg.ReplyMarkup = btns[0]
		}
		msg.ParseMode = tgbotapi.ModeMarkdown
		if mode != "" {
			msg.ParseMode = mode
		}
		send(bot, msg)
		return
	}
	if len(btns) > 0 {
		msg := tgbotapi.NewEditMessageText(GetChatID(update), GetMsgID(update), text)
		msg.ReplyMarkup = &btns[0]
		msg.ParseMode = tgbotapi.ModeMarkdown
		if mode != "" {
			msg.ParseMode = mode
		}
		send(bot, msg)
		return
	}
	msg := tgbotapi.NewMessage(GetChatID(update), text)
	msg.ParseMode = mode
	send(bot, msg)
	return
}

// SendMessage ...
func SendReplyMessage(bot *tgbotapi.BotAPI, update tgbotapi.Update, text string,
	mode string, btns ...tgbotapi.InlineKeyboardMarkup) {

	if update.Message != nil {
		msg := tgbotapi.NewMessage(GetChatID(update), text)
		if len(btns) > 0 {
			msg.ReplyMarkup = btns[0]
		}
		msg.ParseMode = tgbotapi.ModeMarkdown
		if mode != "" {
			msg.ParseMode = mode
		}
		msg.ReplyToMessageID = GetMsgID(update)
		send(bot, msg)
		return
	}
	if len(btns) > 0 {
		msg := tgbotapi.NewEditMessageText(GetChatID(update), GetMsgID(update), text)
		msg.ReplyMarkup = &btns[0]
		msg.ParseMode = tgbotapi.ModeMarkdown
		if mode != "" {
			msg.ParseMode = mode
		}
		send(bot, msg)
		return
	}
	msg := tgbotapi.NewMessage(GetChatID(update), text)
	msg.ParseMode = mode
	send(bot, msg)
	return
}

// GetSigningInfo fetches the full signing info (missed blocks counter,
// jailed_until, tombstoned) for a validator consensus address
func GetSigningInfo(restApi, validatorConsAddress string) (ValSigningInfo, error) {
	url := restApi + fmt.Sprintf("/cosmos/slashing/v1beta1/signing_infos/%s", validatorConsAddress)
	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("Request to %s failed: %v", url, err)
		return ValSigningInfo{}, err
	}
	defer resp.Body.Close()
	var body MissedBlocksResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ValSigningInfo{}, err
	}
	if resp.StatusCode == http.StatusOK {
		return body.ValSigningInfo, nil
	}
	return ValSigningInfo{}, fmt.Errorf("unable to get signing info: %s", resp.Status)
}

func CheckMissedBlocks(restApi, validatorConsAddress string) (int64, error) {
	info, err := GetSigningInfo(restApi, validatorConsAddress)
	if err != nil {
		return 0, err
	}
	missedBlocks, err := strconv.ParseInt(info.MissedBlocksCounter, 10, 64)
	if err != nil {
		log.Printf("Bad missed blocks counter for %s: %v", validatorConsAddress, err)
		return 0, err
	}
	return missedBlocks, nil
}

// GetSlashingParams fetches the slashing window size and the minimum
// fraction of it that must be signed to avoid being jailed
// (min_signed_per_window, e.g. 0.05 for 5%)
func GetSlashingParams(restApi string) (window int64, minSignedPerWindow float64, err error) {
	url := restApi + "/cosmos/slashing/v1beta1/params"
	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("Request to %s failed: %v", url, err)
		return 0, 0, err
	}
	defer resp.Body.Close()
	var body SlashingParamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("unable to get slashing params: %s", resp.Status)
	}
	window, err = strconv.ParseInt(body.Params.SignedBlocksWindow, 10, 64)
	if err != nil {
		return 0, 0, err
	}
	minSignedPerWindow, err = strconv.ParseFloat(body.Params.MinSignedPerWindow, 64)
	if err != nil {
		log.Printf("Bad min_signed_per_window %q from %s: %v", body.Params.MinSignedPerWindow, restApi, err)
		return window, 0, nil
	}
	return window, minSignedPerWindow, nil
}

// GetSignedBlocksWindow fetches the slashing window size of a network
func GetSignedBlocksWindow(restApi string) (int64, error) {
	window, _, err := GetSlashingParams(restApi)
	return window, err
}

// ChainUpgradePlan is a scheduled x/upgrade plan discovered either through a
// governance proposal (voting or passed) or the upgrade module's
// current_plan query
type ChainUpgradePlan struct {
	ProposalID    string // empty when only found via current_plan
	Name          string
	Height        int64
	Info          string
	Status        string    // e.g. "PROPOSAL_STATUS_VOTING_PERIOD" or "PROPOSAL_STATUS_PASSED"
	VotingEndTime time.Time // zero value unless Status is still voting
}

type statusSyncInfo struct {
	LatestBlockHeight string    `json:"latest_block_height"`
	LatestBlockTime   time.Time `json:"latest_block_time"`
}

// statusResponse covers both shapes the /status RPC endpoint is seen to
// return in the wild: classic Tendermint/CometBFT wraps sync_info in a
// JSON-RPC "result" envelope, while some newer CometBFT versions (observed
// on Sei) return sync_info directly at the top level with no envelope.
type statusResponse struct {
	SyncInfo statusSyncInfo `json:"sync_info"`
	Result   struct {
		SyncInfo statusSyncInfo `json:"sync_info"`
	} `json:"result"`
}

// GetLatestBlock fetches the current chain tip height and its block time via
// the CometBFT/Tendermint RPC status endpoint
func GetLatestBlock(rpcApi string) (int64, time.Time, error) {
	url := rpcApi + "/status"
	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("Request to %s failed: %v", url, err)
		return 0, time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, time.Time{}, fmt.Errorf("unable to get node status: %s", resp.Status)
	}
	var body statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, time.Time{}, err
	}
	syncInfo := body.SyncInfo
	if syncInfo.LatestBlockHeight == "" {
		syncInfo = body.Result.SyncInfo
	}
	height, err := strconv.ParseInt(syncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return 0, time.Time{}, err
	}
	return height, syncInfo.LatestBlockTime, nil
}

type currentPlanResponse struct {
	Plan *struct {
		Name   string `json:"name"`
		Height string `json:"height"`
		Info   string `json:"info"`
	} `json:"plan"`
}

// GetCurrentUpgradePlan queries the x/upgrade module's own record of the
// currently scheduled plan. It is authoritative (it's what the chain will
// actually act on) but has no associated proposal ID, so it's used as a
// fallback/cross-check alongside GetSoftwareUpgradeProposals rather than the
// primary discovery source. Returns nil, nil when no plan is scheduled. A
// plan only ever shows up here once its proposal has passed.
func GetCurrentUpgradePlan(restApi string) (*ChainUpgradePlan, error) {
	url := restApi + "/cosmos/upgrade/v1beta1/current_plan"
	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("Request to %s failed: %v", url, err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unable to get current upgrade plan: %s", resp.Status)
	}
	var body currentPlanResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Plan == nil {
		return nil, nil
	}
	height, err := strconv.ParseInt(body.Plan.Height, 10, 64)
	if err != nil || height <= 0 {
		return nil, nil
	}
	return &ChainUpgradePlan{Name: body.Plan.Name, Height: height, Info: body.Plan.Info, Status: govStatusPassed}, nil
}

// govAnyEnvelope decodes the flattened cosmos-sdk Any JSON encoding used for
// both gov v1 proposal messages and gov v1beta1 proposal content, e.g.
// {"@type":"/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade","plan":{...}}
type govAnyEnvelope struct {
	Type string `json:"@type"`
	Plan struct {
		Name   string `json:"name"`
		Height string `json:"height"`
		Info   string `json:"info"`
	} `json:"plan"`
}

// isCancelUpgradeType matches both the gov v1 authority-gated cancel message
// and the legacy v1beta1 cancel proposal content. Must be checked before
// isSoftwareUpgradeType since "CancelSoftwareUpgradeProposal" also contains
// the substring "SoftwareUpgrade".
func isCancelUpgradeType(typeUrl string) bool {
	return strings.Contains(typeUrl, "CancelSoftwareUpgrade") || strings.Contains(typeUrl, "MsgCancelUpgrade")
}

func isSoftwareUpgradeType(typeUrl string) bool {
	return strings.Contains(typeUrl, "SoftwareUpgrade")
}

// gov proposal status query values (shared numbering between gov v1 and
// v1beta1's ProposalStatus enum)
const (
	govStatusVotingPeriod = "2"
	govStatusPassed       = "3"
)

var govProposalStatuses = []string{govStatusVotingPeriod, govStatusPassed}

type govV1ProposalsResponse struct {
	Proposals []struct {
		ID            string            `json:"id"`
		Status        string            `json:"status"`
		VotingEndTime time.Time         `json:"voting_end_time"`
		Messages      []json.RawMessage `json:"messages"`
	} `json:"proposals"`
}

func fetchGovV1Upgrades(restApi, status string) (plans []ChainUpgradePlan, cancelled bool, err error) {
	url := restApi + "/cosmos/gov/v1/proposals?proposal_status=" + status + "&pagination.limit=50&pagination.reverse=true"
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("unable to get gov v1 proposals: %s", resp.Status)
	}
	var body govV1ProposalsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, false, err
	}
	for _, proposal := range body.Proposals {
		for _, raw := range proposal.Messages {
			var msg govAnyEnvelope
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if isCancelUpgradeType(msg.Type) {
				cancelled = true
				continue
			}
			if !isSoftwareUpgradeType(msg.Type) {
				continue
			}
			height, err := strconv.ParseInt(msg.Plan.Height, 10, 64)
			if err != nil || height <= 0 {
				continue
			}
			plans = append(plans, ChainUpgradePlan{
				ProposalID:    proposal.ID,
				Name:          msg.Plan.Name,
				Height:        height,
				Info:          msg.Plan.Info,
				Status:        proposal.Status,
				VotingEndTime: proposal.VotingEndTime,
			})
		}
	}
	return plans, cancelled, nil
}

type govV1Beta1ProposalsResponse struct {
	Proposals []struct {
		ProposalID    string          `json:"proposal_id"`
		Status        string          `json:"status"`
		VotingEndTime time.Time       `json:"voting_end_time"`
		Content       json.RawMessage `json:"content"`
	} `json:"proposals"`
}

func fetchGovV1Beta1Upgrades(restApi, status string) (plans []ChainUpgradePlan, cancelled bool, err error) {
	url := restApi + "/cosmos/gov/v1beta1/proposals?proposal_status=" + status + "&pagination.limit=50&pagination.reverse=true"
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("unable to get gov v1beta1 proposals: %s", resp.Status)
	}
	var body govV1Beta1ProposalsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, false, err
	}
	for _, proposal := range body.Proposals {
		if len(proposal.Content) == 0 {
			continue
		}
		var content govAnyEnvelope
		if err := json.Unmarshal(proposal.Content, &content); err != nil {
			continue
		}
		if isCancelUpgradeType(content.Type) {
			cancelled = true
			continue
		}
		if !isSoftwareUpgradeType(content.Type) {
			continue
		}
		height, err := strconv.ParseInt(content.Plan.Height, 10, 64)
		if err != nil || height <= 0 {
			continue
		}
		plans = append(plans, ChainUpgradePlan{
			ProposalID:    proposal.ProposalID,
			Name:          content.Plan.Name,
			Height:        height,
			Info:          content.Plan.Info,
			Status:        proposal.Status,
			VotingEndTime: proposal.VotingEndTime,
		})
	}
	return plans, cancelled, nil
}

// GetSoftwareUpgradeProposals queries gov v1 proposals, in both the
// voting-period and passed states, for software-upgrade plans, and reports
// whether a cancellation message was seen. gov v1beta1 is only queried as a
// fallback when v1 itself fails for a given status (e.g. older chains that
// never got gov v1, or a chain like Sei that 501s on it) — not run
// alongside v1 unconditionally, since chains that do support v1 (e.g.
// Cosmos Hub) can have a broken/lossy v1beta1 compatibility shim that would
// otherwise fail loudly on every cycle for no reason. Results are deduped
// by proposal ID. This only returns an error if every query fails.
func GetSoftwareUpgradeProposals(restApi string) ([]ChainUpgradePlan, bool, error) {
	var allPlans []ChainUpgradePlan
	var cancelled bool
	var lastErr error
	successCount := 0

	for _, status := range govProposalStatuses {
		plans, c, err := fetchGovV1Upgrades(restApi, status)
		if err == nil {
			successCount++
			allPlans = append(allPlans, plans...)
			cancelled = cancelled || c
			continue
		}
		log.Printf("gov v1 proposals (status=%s) query failed for %s: %v; falling back to gov v1beta1", status, restApi, err)
		lastErr = err

		legacyPlans, legacyCancelled, legacyErr := fetchGovV1Beta1Upgrades(restApi, status)
		if legacyErr != nil {
			log.Printf("gov v1beta1 proposals (status=%s) query failed for %s: %v", status, restApi, legacyErr)
			lastErr = legacyErr
			continue
		}
		successCount++
		allPlans = append(allPlans, legacyPlans...)
		cancelled = cancelled || legacyCancelled
	}

	if successCount == 0 {
		return nil, false, lastErr
	}

	seen := make(map[string]bool)
	var plans []ChainUpgradePlan
	for _, p := range allPlans {
		if seen[p.ProposalID] {
			continue
		}
		seen[p.ProposalID] = true
		plans = append(plans, p)
	}
	return plans, cancelled, nil
}
