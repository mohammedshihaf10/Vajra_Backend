package charging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type AuthorizationClient interface {
	EnsureAuthorization(ctx context.Context, idTag string) error
}

type HasuraAuthorizationClient struct {
	url         string
	adminSecret string
	bearerToken string
	tenantID    string
	idTokenType string
	httpClient  *http.Client
	logger      *slog.Logger
}

type authorizationListResponse struct {
	Data struct {
		Authorizations []authorizationRecord `json:"Authorizations"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type authorizationCreateResponse struct {
	Data struct {
		Authorization *authorizationRecord `json:"insert_Authorizations_one"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type authorizationRecord struct {
	ID          int    `json:"id"`
	IDToken     string `json:"idToken"`
	IDTokenType string `json:"idTokenType"`
	Status      string `json:"status"`
}

const authorizationsListQuery = `query AuthorizationsList($offset: Int!, $limit: Int!, $order_by: [Authorizations_order_by!], $where: Authorizations_bool_exp) {
  Authorizations(
    offset: $offset
    limit: $limit
    order_by: $order_by
    where: $where
  ) {
    id
    idToken
    idTokenType
    status
    groupAuthorizationId
    additionalInfo
    concurrentTransaction
    chargingPriority
    language1
    language2
    personalMessage
    cacheExpiryDateTime
    createdAt
    updatedAt
  }
  Authorizations_aggregate(where: $where) {
    aggregate {
      count
    }
  }
}`

const authorizationsCreateMutation = `mutation AuthorizationsCreate($object: Authorizations_insert_input!) {
  insert_Authorizations_one(object: $object) {
    id
    idToken
    idTokenType
    status
    groupAuthorizationId
    additionalInfo
    concurrentTransaction
    chargingPriority
    language1
    language2
    personalMessage
    cacheExpiryDateTime
    createdAt
    updatedAt
  }
}`

func NewHasuraAuthorizationClient(url string, adminSecret string, bearerToken string, tenantID string, idTokenType string, logger *slog.Logger) *HasuraAuthorizationClient {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(idTokenType) == "" {
		idTokenType = "Local"
	}
	return &HasuraAuthorizationClient{
		url:         strings.TrimSpace(url),
		adminSecret: strings.TrimSpace(adminSecret),
		bearerToken: strings.TrimSpace(bearerToken),
		tenantID:    strings.TrimSpace(tenantID),
		idTokenType: strings.TrimSpace(idTokenType),
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		logger:      logger.With("component", "hasura_authorization"),
	}
}

func (c *HasuraAuthorizationClient) EnsureAuthorization(ctx context.Context, idTag string) error {
	if c == nil || c.url == "" {
		return nil
	}
	if strings.TrimSpace(idTag) == "" {
		return fmt.Errorf("idTag is required")
	}

	existing, err := c.getAuthorizationByIDTag(ctx, idTag)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	var parsed authorizationCreateResponse
	_, err = c.doGraphQL(ctx, graphqlRequest{
		Query: authorizationsCreateMutation,
		Variables: map[string]any{
			"object": map[string]any{
				"idToken":     idTag,
				"idTokenType": c.idTokenType,
				"status":      "Accepted",
				"tenantId":    c.tenantID,
				"createdAt":   now,
				"updatedAt":   now,
			},
		},
		OperationName: "AuthorizationsCreate",
	}, &parsed)
	if err != nil {
		return err
	}
	if parsed.Data.Authorization == nil {
		return fmt.Errorf("authorization creation returned no record")
	}

	c.logger.Info("created authorization for charging idTag", "id_tag", idTag, "authorization_id", parsed.Data.Authorization.ID)
	return nil
}

func (c *HasuraAuthorizationClient) getAuthorizationByIDTag(ctx context.Context, idTag string) (*authorizationRecord, error) {
	var parsed authorizationListResponse
	_, err := c.doGraphQL(ctx, graphqlRequest{
		Query: authorizationsListQuery,
		Variables: map[string]any{
			"limit":  10,
			"offset": 0,
			"order_by": map[string]any{
				"updatedAt": "desc",
			},
			"where": map[string]any{
				"_and": []any{
					map[string]any{
						"idToken": map[string]any{
							"_eq": idTag,
						},
					},
				},
			},
		},
		OperationName: "AuthorizationsList",
	}, &parsed)
	if err != nil {
		return nil, err
	}
	if len(parsed.Data.Authorizations) == 0 {
		return nil, nil
	}
	return &parsed.Data.Authorizations[0], nil
}

func (c *HasuraAuthorizationClient) doGraphQL(ctx context.Context, reqBody graphqlRequest, out any) ([]byte, error) {
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
		return body, fmt.Errorf("hasura authorization request failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return body, err
	}

	switch parsed := out.(type) {
	case *authorizationListResponse:
		if len(parsed.Errors) > 0 {
			return body, fmt.Errorf("hasura graphql error: %s", parsed.Errors[0].Message)
		}
	case *authorizationCreateResponse:
		if len(parsed.Errors) > 0 {
			return body, fmt.Errorf("hasura graphql error: %s", parsed.Errors[0].Message)
		}
	}

	return body, nil
}
