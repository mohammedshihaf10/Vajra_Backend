package ocpp

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

type WebhookClient struct {
	url        string
	secret     string
	httpClient *http.Client
}

func NewWebhookClient(url string, secret string, timeoutSec int) *WebhookClient {
	return &WebhookClient{
		url:    strings.TrimSpace(url),
		secret: secret,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutSec) * time.Second,
		},
	}
}

func (w *WebhookClient) Emit(eventType string, data interface{}) {
	if w.url == "" {
		return
	}

	payload := map[string]interface{}{
		"event_type": eventType,
		"data":       data,
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		log.Printf("webhook: marshal failed for event=%s err=%v", eventType, err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, w.url, bytes.NewReader(buf))
	if err != nil {
		log.Printf("webhook: request build failed for event=%s err=%v", eventType, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if w.secret != "" {
		req.Header.Set("X-OCPP-Signature", "sha256="+hmacSHA256Hex(buf, w.secret))
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		log.Printf("webhook: delivery failed for event=%s err=%v", eventType, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("webhook: non-2xx response for event=%s status=%d", eventType, resp.StatusCode)
	}
}

func hmacSHA256Hex(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
