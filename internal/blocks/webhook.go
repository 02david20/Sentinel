package blocks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HTTPWebhook posts a JSON message {"text": ...} to a webhook URL. The URL may carry a secret, so
// it is read from the environment or ~/.config/sentinel/webhook and never stored in the repo.
type HTTPWebhook struct{ URL string }

func LoadWebhook() (*HTTPWebhook, error) {
	if u := strings.TrimSpace(os.Getenv("SENTINEL_WEBHOOK_URL")); u != "" {
		return &HTTPWebhook{URL: u}, nil
	}
	h, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(h, ".config", "sentinel", "webhook"))
	if err != nil {
		return nil, fmt.Errorf("no webhook (set SENTINEL_WEBHOOK_URL or ~/.config/sentinel/webhook)")
	}
	return &HTTPWebhook{URL: strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])}, nil
}

func (c *HTTPWebhook) Post(ctx context.Context, text string) error {
	if len(text) > 4000 { // many services cap message length
		text = text[:3900] + "\n… (truncated)"
	}
	body, _ := json.Marshal(map[string]string{"text": text})
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if rerr != nil {
		return fmt.Errorf("webhook: the URL is not valid (%s)", redact(rerr, c.URL))
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %w", redact(err, c.URL))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}

// redact keeps the webhook's key and token out of error text and logs.
func redact(err error, url string) error {
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), url, "<webhook>"))
}
