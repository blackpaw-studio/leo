package telegram

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var apiBaseURL = "https://api.telegram.org/bot"

// SendMessage sends a text message to a Telegram chat.
func SendMessage(botToken, chatID, text string, topicID int) error {
	params := url.Values{
		"chat_id":    {chatID},
		"text":       {text},
		"parse_mode": {"Markdown"},
	}
	if topicID > 0 {
		params.Set("message_thread_id", fmt.Sprintf("%d", topicID))
	}

	resp, err := http.PostForm(apiBaseURL+botToken+"/sendMessage", params)
	if err != nil {
		return fmt.Errorf("sending telegram message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var apiErr struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Description != "" {
			return fmt.Errorf("telegram API error (%d): %s", resp.StatusCode, apiErr.Description)
		}
		return fmt.Errorf("telegram API error (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

type updateResponse struct {
	OK     bool `json:"ok"`
	Result []struct {
		Message struct {
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
	} `json:"result"`
}

// PollChatID polls getUpdates to detect the chat ID from the first message received.
func PollChatID(botToken string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, err := http.Get(apiBaseURL + botToken + "/getUpdates?timeout=5&limit=1")
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}

		var result updateResponse
		if err := json.Unmarshal(body, &result); err != nil {
			continue
		}

		if result.OK && len(result.Result) > 0 {
			chatID := fmt.Sprintf("%d", result.Result[0].Message.Chat.ID)
			return chatID, nil
		}

		time.Sleep(2 * time.Second)
	}

	return "", fmt.Errorf("timed out waiting for a message — send any message to your bot")
}
