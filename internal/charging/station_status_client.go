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

type StationStatusClient interface {
	GetConnectorStatus(ctx context.Context, chargerID string, connectorID int) (*models.ConnectorStatus, error)
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
	ChargingStation *chargingStation `json:"ChargingStations_by_pk"`
}

type chargingStation struct {
	ID                        string                     `json:"id"`
	IsOnline                  bool                       `json:"isOnline"`
	Protocol                  string                     `json:"protocol"`
	ChargePointVendor         string                     `json:"chargePointVendor"`
	ChargePointModel          string                     `json:"chargePointModel"`
	Connectors                []graphqlConnector         `json:"Connectors"`
	LatestStatusNotifications []latestStatusNotification `json:"LatestStatusNotifications"`
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

	reqBody := graphqlRequest{
		Query:         stationStatusQuery,
		Variables:     map[string]any{"id": chargerID},
		OperationName: "GetChargingStationById",
	}
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
	log.Printf("HASURA RESPONSE charger=%s connector=%d status=%d body=%s", chargerID, connectorID, resp.StatusCode, truncateLogBody(string(body), 8000))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("hasura status request failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed graphqlResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("hasura graphql error: %s", parsed.Errors[0].Message)
	}
	if parsed.Data.ChargingStation == nil {
		return nil, nil
	}

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
