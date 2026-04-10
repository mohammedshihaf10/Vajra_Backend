package charging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"vajraBackend/internal/models"
)

const stationStatusQuery = `query GetChargingStationById($id: String!) {
  ChargingStations_by_pk(id: $id) {
    id
    isOnline
    protocol
    locationId
    chargePointVendor
    chargePointModel
    createdAt
    updatedAt
    LatestStatusNotifications {
      id
      stationId
      statusNotificationId
      updatedAt
      createdAt
      StatusNotification {
        connectorId
        connectorStatus
        createdAt
        evseId
        stationId
        id
        timestamp
        updatedAt
      }
    }
    Connectors {
      id
      stationId
      evseId
      connectorId
      status
      type
      maximumPowerWatts
      maximumAmperage
      maximumVoltage
      format
      powerType
      termsAndConditionsUrl
      errorCode
      timestamp
      info
      vendorId
      vendorErrorCode
      createdAt
      updatedAt
    }
  }
}`

const transactionByRemoteStartIDQuery = `query GetTransactionByRemoteStartId($remoteStartId: Int!) {
  Transactions(
    where: {remoteStartId: {_eq: $remoteStartId}}
    order_by: {createdAt: asc}
    limit: 2
  ) {
    id
    stationId
    transactionId
    remoteStartId
    isActive
    chargingState
    totalKwh
    evseId
    createdAt
    updatedAt
    Connector {
      connectorId
    }
  }
}`

const transactionFallbackQuery = `query GetTransactionFallback($stationId: String!, $createdAt: timestamptz!, $connectorId: Int) {
  Transactions(
    where: {
      stationId: {_eq: $stationId},
      createdAt: {_gte: $createdAt},
      _or: [
        {Connector: {connectorId: {_eq: $connectorId}}},
        {Connector: {connectorId: {_is_null: true}}}
      ]
    }
    order_by: {createdAt: asc}
    limit: 2
  ) {
    id
    stationId
    transactionId
    remoteStartId
    isActive
    chargingState
    totalKwh
    evseId
    createdAt
    updatedAt
    Connector {
      connectorId
    }
  }
}`

const transactionByIDQuery = `query GetTransactionById($id: Int!) {
  Transactions_by_pk(id: $id) {
    id
    timeSpentCharging
    isActive
    chargingState
    stationId
    stoppedReason
    transactionId
    evseId
    remoteStartId
    totalKwh
    createdAt
    updatedAt
    Connector {
      connectorId
    }
  }
}`

type StationStatusClient interface {
	GetConnectorStatus(ctx context.Context, chargerID string, connectorID int) (*models.ConnectorStatus, error)
	GetTransactionByRemoteStartID(ctx context.Context, remoteStartID int) (*ActiveTransaction, error)
	FindFallbackTransactions(ctx context.Context, chargerID string, connectorID int, createdAt string) ([]ActiveTransaction, error)
	GetTransactionByID(ctx context.Context, id int) (*ChargingTransaction, error)
}

type ActiveTransaction struct {
	ID            int
	StationID     string
	TransactionID string
	ConnectorID   int
	RemoteStartID int
	CreatedAt     string
	IsActive      bool
	ChargingState string
}

type ChargingTransaction struct {
	ID                int
	TimeSpentCharging int
	IsActive          bool
	ChargingState     string
	StationID         string
	StoppedReason     string
	TransactionID     string
	EVSEID            *int
	RemoteStartID     int
	TotalKwh          float64
	CreatedAt         string
	UpdatedAt         string
	ConnectorID       int
}

type HasuraStationStatusClient struct {
	url         string
	adminSecret string
	bearerToken string
	httpClient  *http.Client
	logger      *slog.Logger
}

type graphqlRequest struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
	OperationName string         `json:"operationName,omitempty"`
}

type graphqlResponse struct {
	Data   stationStatusData `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type stationStatusData struct {
	ChargingStation *chargingStation    `json:"ChargingStations_by_pk"`
	Transactions    []graphqlTxSummary  `json:"Transactions"`
	TransactionByPK *graphqlTransaction `json:"Transactions_by_pk"`
}

type chargingStation struct {
	ID                        string                     `json:"id"`
	IsOnline                  bool                       `json:"isOnline"`
	Protocol                  string                     `json:"protocol"`
	ChargePointVendor         string                     `json:"chargePointVendor"`
	ChargePointModel          string                     `json:"chargePointModel"`
	Connectors                []graphqlConnector         `json:"Connectors"`
	LatestStatusNotifications []latestStatusNotification `json:"LatestStatusNotifications"`
	Transactions              []graphqlTransaction       `json:"Transactions"`
}

type graphqlTransaction struct {
	ID                int     `json:"id"`
	TimeSpentCharging int     `json:"timeSpentCharging"`
	IsActive          bool    `json:"isActive"`
	ChargingState     string  `json:"chargingState"`
	StationID         string  `json:"stationId"`
	StoppedReason     string  `json:"stoppedReason"`
	TransactionID     string  `json:"transactionId"`
	EVSEID            *int    `json:"evseId"`
	RemoteStartID     int     `json:"remoteStartId"`
	TotalKwh          float64 `json:"totalKwh"`
	CreatedAt         string  `json:"createdAt"`
	UpdatedAt         string  `json:"updatedAt"`
	Connector         struct {
		ConnectorID int `json:"connectorId"`
	} `json:"Connector"`
}

type graphqlTxSummary struct {
	ID            int     `json:"id"`
	StationID     string  `json:"stationId"`
	TransactionID string  `json:"transactionId"`
	RemoteStartID int     `json:"remoteStartId"`
	IsActive      bool    `json:"isActive"`
	ChargingState string  `json:"chargingState"`
	TotalKwh      float64 `json:"totalKwh"`
	EVSEID        *int    `json:"evseId"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
	Connector     struct {
		ConnectorID int `json:"connectorId"`
	} `json:"Connector"`
}

type graphqlConnector struct {
	EVSEID      *int   `json:"evseId"`
	ConnectorID int    `json:"connectorId"`
	Status      string `json:"status"`
	Timestamp   string `json:"timestamp"`
	ErrorCode   string `json:"errorCode"`
	Info        string `json:"info"`
}

type latestStatusNotification struct {
	StatusNotification struct {
		EVSEID          *int   `json:"evseId"`
		ConnectorID     int    `json:"connectorId"`
		ConnectorStatus string `json:"connectorStatus"`
		Timestamp       string `json:"timestamp"`
	} `json:"StatusNotification"`
}

func NewHasuraStationStatusClient(url string, adminSecret string, bearerToken string, logger *slog.Logger) *HasuraStationStatusClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &HasuraStationStatusClient{
		url:         strings.TrimSpace(url),
		adminSecret: strings.TrimSpace(adminSecret),
		bearerToken: strings.TrimSpace(bearerToken),
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		logger:      logger.With("component", "hasura_station_status"),
	}
}

func (c *HasuraStationStatusClient) GetConnectorStatus(ctx context.Context, chargerID string, connectorID int) (*models.ConnectorStatus, error) {
	if c.url == "" {
		return nil, nil
	}

	var parsed graphqlResponse
	body, err := c.doGraphQL(ctx, graphqlRequest{
		Query:         stationStatusQuery,
		Variables:     map[string]any{"id": chargerID},
		OperationName: "GetChargingStationById",
	}, &parsed)
	if err != nil {
		return nil, err
	}
	if parsed.Data.ChargingStation == nil {
		return nil, nil
	}

	log.Printf("HASURA RESPONSE charger=%s connector=%d body=%s", chargerID, connectorID, truncateLogBody(string(body), 8000))
	status := c.pickConnectorStatus(parsed.Data.ChargingStation, chargerID, connectorID)
	if status != nil {
		c.logger.Info("resolved connector status from graphql",
			"charger_id", chargerID,
			"connector_id", connectorID,
			"status", status.Status,
		)
	}
	return status, nil
}

func (c *HasuraStationStatusClient) GetTransactionByRemoteStartID(ctx context.Context, remoteStartID int) (*ActiveTransaction, error) {
	if c.url == "" || remoteStartID <= 0 {
		return nil, nil
	}

	var parsed graphqlResponse
	body, err := c.doGraphQL(ctx, graphqlRequest{
		Query:         transactionByRemoteStartIDQuery,
		Variables:     map[string]any{"remoteStartId": remoteStartID},
		OperationName: "GetTransactionByRemoteStartId",
	}, &parsed)
	if err != nil {
		return nil, err
	}

	log.Printf("HASURA TRANSACTION BY REMOTE START remote_start_id=%d body=%s", remoteStartID, truncateLogBody(string(body), 8000))
	if len(parsed.Data.Transactions) == 0 {
		return nil, nil
	}
	if len(parsed.Data.Transactions) > 1 {
		return nil, fmt.Errorf("multiple transactions matched remoteStartId=%d", remoteStartID)
	}
	return toActiveTransaction(parsed.Data.Transactions[0]), nil
}

func (c *HasuraStationStatusClient) FindFallbackTransactions(ctx context.Context, chargerID string, connectorID int, createdAt string) ([]ActiveTransaction, error) {
	if c.url == "" || strings.TrimSpace(chargerID) == "" || strings.TrimSpace(createdAt) == "" {
		return nil, nil
	}

	var parsed graphqlResponse
	body, err := c.doGraphQL(ctx, graphqlRequest{
		Query: transactionFallbackQuery,
		Variables: map[string]any{
			"stationId":   chargerID,
			"createdAt":   createdAt,
			"connectorId": connectorID,
		},
		OperationName: "GetTransactionFallback",
	}, &parsed)
	if err != nil {
		return nil, err
	}

	log.Printf("HASURA TRANSACTION FALLBACK charger=%s connector=%d created_at=%s body=%s", chargerID, connectorID, createdAt, truncateLogBody(string(body), 8000))
	result := make([]ActiveTransaction, 0, len(parsed.Data.Transactions))
	for _, tx := range parsed.Data.Transactions {
		if tx.ID <= 0 {
			continue
		}
		if connectorID > 0 && tx.Connector.ConnectorID > 0 && tx.Connector.ConnectorID != connectorID {
			continue
		}
		result = append(result, *toActiveTransaction(tx))
	}
	return result, nil
}

func (c *HasuraStationStatusClient) GetTransactionByID(ctx context.Context, id int) (*ChargingTransaction, error) {
	if c.url == "" || id <= 0 {
		return nil, nil
	}

	var parsed graphqlResponse
	body, err := c.doGraphQL(ctx, graphqlRequest{
		Query:         transactionByIDQuery,
		Variables:     map[string]any{"id": id},
		OperationName: "GetTransactionById",
	}, &parsed)
	if err != nil {
		return nil, err
	}
	if parsed.Data.TransactionByPK == nil {
		return nil, nil
	}

	log.Printf("HASURA TRANSACTION BY ID id=%d body=%s", id, truncateLogBody(string(body), 8000))
	tx := parsed.Data.TransactionByPK
	return &ChargingTransaction{
		ID:                tx.ID,
		TimeSpentCharging: tx.TimeSpentCharging,
		IsActive:          tx.IsActive,
		ChargingState:     strings.TrimSpace(tx.ChargingState),
		StationID:         strings.TrimSpace(tx.StationID),
		StoppedReason:     strings.TrimSpace(tx.StoppedReason),
		TransactionID:     strings.TrimSpace(tx.TransactionID),
		EVSEID:            tx.EVSEID,
		RemoteStartID:     tx.RemoteStartID,
		TotalKwh:          tx.TotalKwh,
		CreatedAt:         tx.CreatedAt,
		UpdatedAt:         tx.UpdatedAt,
		ConnectorID:       tx.Connector.ConnectorID,
	}, nil
}

func (c *HasuraStationStatusClient) doGraphQL(ctx context.Context, reqBody graphqlRequest, out any) ([]byte, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.adminSecret != "" {
		req.Header.Set("x-hasura-admin-secret", c.adminSecret)
	}
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, fmt.Errorf("hasura status request failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return body, err
	}

	if parsed, ok := out.(*graphqlResponse); ok && len(parsed.Errors) > 0 {
		return body, fmt.Errorf("hasura graphql error: %s", parsed.Errors[0].Message)
	}
	return body, nil
}

func (c *HasuraStationStatusClient) pickConnectorStatus(station *chargingStation, chargerID string, connectorID int) *models.ConnectorStatus {
	for _, connector := range station.Connectors {
		if connector.ConnectorID != connectorID {
			continue
		}
		metadata, _ := json.Marshal(map[string]any{
			"source":             "hasura_graphql",
			"isOnline":           station.IsOnline,
			"protocol":           station.Protocol,
			"chargePointVendor":  station.ChargePointVendor,
			"chargePointModel":   station.ChargePointModel,
			"connectorErrorCode": connector.ErrorCode,
			"connectorInfo":      connector.Info,
		})
		return &models.ConnectorStatus{
			ChargerID:   chargerID,
			ConnectorID: connectorID,
			EVSEID:      connector.EVSEID,
			Status:      connector.Status,
			LastSeen:    connector.Timestamp,
			Metadata:    string(metadata),
		}
	}

	for _, notification := range station.LatestStatusNotifications {
		status := notification.StatusNotification
		if status.ConnectorID != connectorID {
			continue
		}
		metadata, _ := json.Marshal(map[string]any{
			"source":            "hasura_graphql_latest_status_notification",
			"isOnline":          station.IsOnline,
			"protocol":          station.Protocol,
			"chargePointVendor": station.ChargePointVendor,
			"chargePointModel":  station.ChargePointModel,
		})
		return &models.ConnectorStatus{
			ChargerID:   chargerID,
			ConnectorID: connectorID,
			EVSEID:      status.EVSEID,
			Status:      status.ConnectorStatus,
			LastSeen:    status.Timestamp,
			Metadata:    string(metadata),
		}
	}

	return nil
}

func truncateLogBody(body string, limit int) string {
	if limit <= 0 || len(body) <= limit {
		return body
	}
	return body[:limit] + "...(truncated)"
}

func toActiveTransaction(tx graphqlTxSummary) *ActiveTransaction {
	return &ActiveTransaction{
		ID:            tx.ID,
		StationID:     strings.TrimSpace(tx.StationID),
		TransactionID: strings.TrimSpace(tx.TransactionID),
		ConnectorID:   tx.Connector.ConnectorID,
		RemoteStartID: tx.RemoteStartID,
		CreatedAt:     tx.CreatedAt,
		IsActive:      tx.IsActive,
		ChargingState: strings.TrimSpace(tx.ChargingState),
	}
}
