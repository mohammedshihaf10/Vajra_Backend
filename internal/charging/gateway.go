package charging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type GatewayClient interface {
	IsConnected(ctx context.Context, chargerID string) (bool, error)
	GetConnectorStatus(ctx context.Context, chargerID string, connectorID int) (string, bool, error)
	RemoteStart(ctx context.Context, sessionID string, chargerID string, connectorID int, idTag string) error
	RemoteStop(ctx context.Context, sessionID string, chargerID string, transactionID int) error
}

type HTTPGatewayClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewHTTPGatewayClient(baseURL string, apiKey string) *HTTPGatewayClient {
	return &HTTPGatewayClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *HTTPGatewayClient) IsConnected(ctx context.Context, chargerID string) (bool, error) {
	path := fmt.Sprintf("/v1/chargers/%s/connectivity", url.PathEscape(chargerID))
	var resp struct {
		Connected bool `json:"connected"`
	}
	if err := c.request(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return false, err
	}
	return resp.Connected, nil
}

func (c *HTTPGatewayClient) GetConnectorStatus(ctx context.Context, chargerID string, connectorID int) (string, bool, error) {
	path := fmt.Sprintf("/v1/chargers/%s/connectors/%d/status", url.PathEscape(chargerID), connectorID)
	var resp struct {
		Status string `json:"status"`
		Known  bool   `json:"known"`
	}
	if err := c.request(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return "", false, err
	}
	return resp.Status, resp.Known, nil
}

func (c *HTTPGatewayClient) RemoteStart(ctx context.Context, sessionID string, chargerID string, connectorID int, idTag string) error {
	body := map[string]interface{}{
		"session_id":   sessionID,
		"charger_id":   chargerID,
		"connector_id": connectorID,
		"id_tag":       idTag,
	}
	return c.request(ctx, http.MethodPost, "/v1/commands/remote-start", body, nil)
}

func (c *HTTPGatewayClient) RemoteStop(ctx context.Context, sessionID string, chargerID string, transactionID int) error {
	body := map[string]interface{}{
		"session_id":     sessionID,
		"charger_id":     chargerID,
		"transaction_id": transactionID,
	}
	return c.request(ctx, http.MethodPost, "/v1/commands/remote-stop", body, nil)
}

func (c *HTTPGatewayClient) request(ctx context.Context, method string, path string, reqBody interface{}, out interface{}) error {
	var bodyReader *bytes.Reader
	if reqBody != nil {
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(buf)
	} else {
		bodyReader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ocpp service request failed: %s %s -> %d", method, path, resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
