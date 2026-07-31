package helpers

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

// httpClient is a dedicated client with a timeout so a stuck REST endpoint
// cannot hang a whole check cycle
var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
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

// GetSignedBlocksWindow fetches the slashing window size of a network
func GetSignedBlocksWindow(restApi string) (int64, error) {
	url := restApi + "/cosmos/slashing/v1beta1/params"
	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("Request to %s failed: %v", url, err)
		return 0, err
	}
	defer resp.Body.Close()
	var body SlashingParamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	if resp.StatusCode == http.StatusOK {
		return strconv.ParseInt(body.Params.SignedBlocksWindow, 10, 64)
	}
	return 0, fmt.Errorf("unable to get slashing params: %s", resp.Status)
}
