package charging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type CitrineClient interface {
	StartTransaction(ctx context.Context, req StartTransactionRequest) error
	StopTransaction(ctx context.Context, req StopTransactionRequest) error
	UnlockConnector(ctx context.Context, req UnlockConnectorRequest) error
	GetTransaction(ctx context.Context, req GetTransactionRequest) (*TransactionRecord, error)
}

type CitrineConfig struct {
	BaseURL          string
	APIKey           string
	TenantID         int
	CallbackURL      string
	Timeout          time.Duration
	MaxRetries       int
	BackoffInitial   time.Duration
	BackoffMax       time.Duration
	CircuitThreshold int
	CircuitCooldown  time.Duration
}

type StartTransactionRequest struct {
	Identifier      string         `json:"-"`
	IDTag           string         `json:"idTag"`
	ConnectorID     *int           `json:"connectorId,omitempty"`
	RemoteStartID   string         `json:"remoteStartId,omitempty"`
	ChargingProfile map[string]any `json:"chargingProfile,omitempty"`
}

type StopTransactionRequest struct {
	Identifier    string `json:"-"`
	TransactionID int    `json:"transactionId"`
}

type UnlockConnectorRequest struct {
	Identifier  string `json:"-"`
	EVSEID      int    `json:"evseId"`
	ConnectorID int    `json:"connectorId"`
}

type GetTransactionRequest struct {
	StationID     string
	TransactionID string
}

type IDToken struct {
	IDToken        string              `json:"idToken"`
	Type           string              `json:"type"`
	AdditionalInfo []map[string]string `json:"additionalInfo,omitempty"`
}

type TransactionRecord struct {
	TransactionID string         `json:"transactionId"`
	StationID     string         `json:"stationId"`
	IsActive      bool           `json:"isActive"`
	TotalKwh      float64        `json:"totalKwh"`
	TotalCost     float64        `json:"totalCost"`
	StartTime     string         `json:"startTime"`
	EndTime       string         `json:"endTime"`
	Station       map[string]any `json:"station"`
	EVSE          map[string]any `json:"evse"`
	Connector     map[string]any `json:"connector"`
	MeterValues   []any          `json:"meterValues"`
}

type commandDispatchResult struct {
	Success bool `json:"success"`
	Payload any  `json:"payload"`
}

type HTTPCitrineClient struct {
	baseURL     string
	apiKey      string
	tenant      int
	callbackURL string
	logger      *slog.Logger
	client      *http.Client

	maxRetries       int
	backoffInitial   time.Duration
	backoffMax       time.Duration
	circuitThreshold int
	circuitCooldown  time.Duration

	consecutiveFailures int
	circuitOpenedAt     time.Time
}

func NewHTTPCitrineClient(cfg CitrineConfig, logger *slog.Logger) *HTTPCitrineClient {
	if logger == nil {
		logger = slog.Default()
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	backoffInitial := cfg.BackoffInitial
	if backoffInitial <= 0 {
		backoffInitial = 500 * time.Millisecond
	}
	backoffMax := cfg.BackoffMax
	if backoffMax <= 0 {
		backoffMax = 4 * time.Second
	}
	circuitThreshold := cfg.CircuitThreshold
	if circuitThreshold <= 0 {
		circuitThreshold = 5
	}
	circuitCooldown := cfg.CircuitCooldown
	if circuitCooldown <= 0 {
		circuitCooldown = 15 * time.Second
	}

	return &HTTPCitrineClient{
		baseURL:          strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:           cfg.APIKey,
		tenant:           cfg.TenantID,
		callbackURL:      cfg.CallbackURL,
		logger:           logger.With("component", "citrine_client"),
		client:           &http.Client{Timeout: timeout},
		maxRetries:       maxRetries,
		backoffInitial:   backoffInitial,
		backoffMax:       backoffMax,
		circuitThreshold: circuitThreshold,
		circuitCooldown:  circuitCooldown,
	}
}

func (c *HTTPCitrineClient) StartTransaction(ctx context.Context, req StartTransactionRequest) error {
	query := url.Values{}
	query.Set("identifier", req.Identifier)
	query.Set("tenantId", strconv.Itoa(c.tenant))
	if c.callbackURL != "" {
		query.Set("callbackUrl", c.callbackURL)
	}
	return c.dispatchCommand(ctx, http.MethodPost, "/ocpp/1.6/evdriver/remoteStartTransaction", query, req)
}

func (c *HTTPCitrineClient) StopTransaction(ctx context.Context, req StopTransactionRequest) error {
	query := url.Values{}
	query.Set("identifier", req.Identifier)
	query.Set("tenantId", strconv.Itoa(c.tenant))
	if c.callbackURL != "" {
		query.Set("callbackUrl", c.callbackURL)
	}
	return c.dispatchCommand(ctx, http.MethodPost, "/ocpp/1.6/evdriver/remoteStopTransaction", query, req)
}

func (c *HTTPCitrineClient) UnlockConnector(ctx context.Context, req UnlockConnectorRequest) error {
	query := url.Values{}
	query.Set("identifier", req.Identifier)
	query.Set("tenantId", strconv.Itoa(c.tenant))
	if c.callbackURL != "" {
		query.Set("callbackUrl", c.callbackURL)
	}
	return c.dispatchCommand(ctx, http.MethodPost, "/ocpp/1.6/evdriver/unlockConnector", query, req)
}

func (c *HTTPCitrineClient) GetTransaction(ctx context.Context, req GetTransactionRequest) (*TransactionRecord, error) {
	query := url.Values{}
	query.Set("stationId", req.StationID)
	query.Set("transactionId", req.TransactionID)
	query.Set("tenantId", strconv.Itoa(c.tenant))

	var out TransactionRecord
	if err := c.doJSON(ctx, http.MethodGet, "/data/transactions/transaction", query, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *HTTPCitrineClient) dispatchCommand(ctx context.Context, method string, path string, query url.Values, body any) error {
	var results []commandDispatchResult
	if err := c.doJSON(ctx, method, path, query, body, &results); err != nil {
		return err
	}
	if len(results) == 0 {
		return errors.New("citrine command returned no dispatch results")
	}
	for _, result := range results {
		if !result.Success {
			return fmt.Errorf("citrine command was not accepted: %v", result.Payload)
		}
	}
	return nil
}

func (c *HTTPCitrineClient) doJSON(ctx context.Context, method string, path string, query url.Values, reqBody any, out any) error {
	if err := c.beforeRequest(); err != nil {
		return err
	}

	var payload []byte
	var err error
	if reqBody != nil {
		payload, err = json.Marshal(reqBody)
		if err != nil {
			return err
		}
	}

	var attempt int
	for {
		attempt++
		reqURL := c.baseURL + path
		if len(query) > 0 {
			reqURL += "?" + query.Encode()
		}

		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}

		req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.apiKey != "" {
			req.Header.Set("X-API-Key", c.apiKey)
		}

		start := time.Now()
		resp, err := c.client.Do(req)
		duration := time.Since(start)

		if err != nil {
			c.recordFailure()
			c.logger.Warn("citrine request failed",
				"method", method,
				"path", path,
				"attempt", attempt,
				"duration_ms", duration.Milliseconds(),
				"error", err,
			)
			if attempt >= c.maxRetries || !isRetriableTransport(err) {
				return err
			}
			if waitErr := sleepWithBackoff(ctx, attempt, c.backoffInitial, c.backoffMax); waitErr != nil {
				return waitErr
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			c.recordFailure()
			return readErr
		}

		c.logger.Info("citrine request completed",
			"method", method,
			"path", path,
			"attempt", attempt,
			"status_code", resp.StatusCode,
			"duration_ms", duration.Milliseconds(),
		)

		if resp.StatusCode >= 500 {
			c.recordFailure()
			if attempt >= c.maxRetries {
				return fmt.Errorf("citrine request failed: %s %s -> %d", method, path, resp.StatusCode)
			}
			if waitErr := sleepWithBackoff(ctx, attempt, c.backoffInitial, c.backoffMax); waitErr != nil {
				return waitErr
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			c.resetFailures()
			return fmt.Errorf("citrine request failed: %s %s -> %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
		}

		c.resetFailures()
		if out == nil || len(respBody) == 0 {
			return nil
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode citrine response: %w", err)
		}
		return nil
	}
}

func (c *HTTPCitrineClient) beforeRequest() error {
	if c.consecutiveFailures < c.circuitThreshold {
		return nil
	}
	if time.Since(c.circuitOpenedAt) >= c.circuitCooldown {
		c.resetFailures()
		return nil
	}
	return fmt.Errorf("citrine circuit breaker open")
}

func (c *HTTPCitrineClient) recordFailure() {
	c.consecutiveFailures++
	if c.consecutiveFailures >= c.circuitThreshold && c.circuitOpenedAt.IsZero() {
		c.circuitOpenedAt = time.Now()
	}
}

func (c *HTTPCitrineClient) resetFailures() {
	c.consecutiveFailures = 0
	c.circuitOpenedAt = time.Time{}
}

func isRetriableTransport(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF)
}

func sleepWithBackoff(ctx context.Context, attempt int, base, max time.Duration) error {
	backoff := float64(base) * math.Pow(2, float64(attempt-1))
	wait := time.Duration(backoff)
	if wait > max {
		wait = max
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
