package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
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

// Topic represents a discovered forum topic.
type Topic struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// FetchTopics discovers forum topics by calling getForumTopicInfo for known thread IDs
// found via getUpdates. It returns unique topics sorted by ID.
func FetchTopics(ctx context.Context, botToken, groupID string) ([]Topic, error) {
	// Get recent updates to find message_thread_id values
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		apiBaseURL+botToken+"/getUpdates?timeout=1&limit=100", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching updates: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var result struct {
		OK     bool `json:"ok"`
		Result []struct {
			Message *topicMessage `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("telegram API returned ok=false")
	}

	seen := make(map[int]string)
	for _, u := range result.Result {
		if u.Message == nil {
			continue
		}
		chatID := fmt.Sprintf("%d", u.Message.Chat.ID)
		if chatID != groupID {
			continue
		}
		if u.Message.MessageThreadID > 0 {
			// Use forum_topic_created name if available, otherwise keep existing
			if u.Message.ForumTopicCreated != nil {
				seen[u.Message.MessageThreadID] = u.Message.ForumTopicCreated.Name
			} else if _, ok := seen[u.Message.MessageThreadID]; !ok {
				seen[u.Message.MessageThreadID] = ""
			}
		}
	}

	var topics []Topic
	for id, name := range seen {
		topics = append(topics, Topic{ID: id, Name: name})
	}

	// Sort by ID for stable output
	sortTopics(topics)

	return topics, nil
}

func sortTopics(topics []Topic) {
	sort.Slice(topics, func(i, j int) bool {
		return topics[i].ID < topics[j].ID
	})
}

type topicMessage struct {
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	MessageThreadID   int    `json:"message_thread_id"`
	Text              string `json:"text"`
	ForumTopicCreated *struct {
		Name string `json:"name"`
	} `json:"forum_topic_created"`
}

// WriteTopicCache writes topics to a JSON cache file.
func WriteTopicCache(path string, topics []Topic) error {
	data, err := json.MarshalIndent(topics, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling topics: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// ReadTopicCache reads topics from a JSON cache file.
func ReadTopicCache(path string) ([]Topic, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var topics []Topic
	if err := json.Unmarshal(data, &topics); err != nil {
		return nil, fmt.Errorf("parsing topic cache: %w", err)
	}
	return topics, nil
}

// PollChatID polls getUpdates to detect the chat ID from the first message received.
// It derives a context with the given timeout; use PollChatIDCtx for explicit context control.
func PollChatID(botToken string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return PollChatIDCtx(ctx, botToken)
}

// PollChatIDCtx polls getUpdates to detect the chat ID, respecting the given context.
func PollChatIDCtx(ctx context.Context, botToken string) (string, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+botToken+"/getUpdates?timeout=5&limit=1", nil)
		if err != nil {
			return "", fmt.Errorf("creating request: %w", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("timed out waiting for a message — send any message to your bot")
			}
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("timed out waiting for a message — send any message to your bot")
			case <-ticker.C:
				continue
			}
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

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out waiting for a message — send any message to your bot")
		case <-ticker.C:
		}
	}
}
