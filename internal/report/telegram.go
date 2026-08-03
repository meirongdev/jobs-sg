package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Telegram pushes a weekly summary to a content topic (never the alert topic,
// docs/02 §4.3). Zero-value (no token/chat) is a no-op — local runs skip.
type Telegram struct {
	Token    string
	ChatID   string
	ThreadID string // message_thread_id; leave empty for the general topic
	Client   *http.Client
	// apiBase overrides the Telegram endpoint. Test seam only — production always
	// uses the default, so there is no way to misconfigure it from the outside.
	apiBase string
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
	// message_thread_id must be a JSON number: the Bot API types it as Integer, and
	// a quoted string risks being rejected or ignored — the latter would silently
	// post the weekly report into the group's General topic instead of the content
	// topic, which is the one outcome docs/02 §4.3 explicitly forbids (the alert
	// topic and the content topic share one chat and differ only by this field).
	if t.ThreadID != "" {
		id, err := strconv.Atoi(strings.TrimSpace(t.ThreadID))
		if err != nil {
			return fmt.Errorf("telegram thread id %q is not an integer: %w", t.ThreadID, err)
		}
		payload["message_thread_id"] = id
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	base := t.apiBase
	if base == "" {
		base = "https://api.telegram.org/bot" + t.Token
	}
	url := base + "/sendMessage"
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
