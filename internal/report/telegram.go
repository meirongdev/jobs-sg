package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Telegram pushes a weekly summary to a content topic (never the alert topic,
// docs/02 §4.3). Zero-value (no token/chat) is a no-op — local runs skip.
type Telegram struct {
	Token    string
	ChatID   string
	ThreadID string // message_thread_id; leave empty for the general topic
	Client   *http.Client
}

// Enabled reports whether a push will actually be attempted.
func (t *Telegram) Enabled() bool {
	return t.Token != "" && t.ChatID != ""
}

// SendSummary posts the report summary markdown to the configured chat/topic.
func (t *Telegram) SendSummary(ctx context.Context, text string) error {
	if !t.Enabled() {
		return nil
	}
	payload := map[string]any{
		"chat_id":    t.ChatID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	if t.ThreadID != "" {
		payload["message_thread_id"] = t.ThreadID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram http %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}
