package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

type PublicHandler struct {
	db *sqlx.DB
}

func NewPublicHandler(db *sqlx.DB) *PublicHandler {
	return &PublicHandler{db: db}
}

var publicTableQueries = map[string]string{
	"chargers": `
		SELECT id, status, last_seen, model, vendor
		FROM chargers
		ORDER BY last_seen DESC NULLS LAST, id ASC
		LIMIT $1 OFFSET $2
	`,
	"charging_sessions": `
		SELECT id, charger_id, connector_id, start_time, end_time, energy_kwh, cost, status, transaction_id
		FROM charging_sessions
		ORDER BY start_time DESC, id DESC
		LIMIT $1 OFFSET $2
	`,
	"phone_otps": `
		SELECT id, phone_number, code, expires_at, created_at, updated_at
		FROM phone_otps
		ORDER BY created_at DESC, id DESC
		LIMIT $1 OFFSET $2
	`,
	"error_logs": `
		SELECT id, request_id, method, path, route, query_string, status_code, error_message,
		       request_body, response_body, user_id, client_ip, user_agent, stack_trace, occurred_at
		FROM error_logs
		ORDER BY occurred_at DESC, id DESC
		LIMIT $1 OFFSET $2
	`,
}

// ListPublicTableData godoc
// @Summary List public table data
// @Description Read-only public endpoint for whitelisted tables
// @Tags public
// @Produce json
// @Param table query string true "Public table name"
// @Param limit query int false "Rows per page" default(20)
// @Param offset query int false "Rows to skip" default(0)
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /public/data [get]
func (h *PublicHandler) ListPublicTableData(c *gin.Context) {
	table := c.Query("table")
	query, ok := publicTableQueries[table]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid table; allowed values: chargers, charging_sessions, phone_otps, error_logs",
		})
		return
	}

	limit := 20
	if rawLimit := c.DefaultQuery("limit", "20"); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil || parsedLimit <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return
		}
		if parsedLimit > 100 {
			parsedLimit = 100
		}
		limit = parsedLimit
	}

	offset := 0
	if rawOffset := c.DefaultQuery("offset", "0"); rawOffset != "" {
		parsedOffset, err := strconv.Atoi(rawOffset)
		if err != nil || parsedOffset < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a non-negative integer"})
			return
		}
		offset = parsedOffset
	}

	rows, err := h.db.Queryx(query, limit, offset)
	if err != nil {
		respondWithInternalError(c, "failed to query table", err)
		return
	}
	defer rows.Close()

	data := make([]map[string]interface{}, 0)
	for rows.Next() {
		row := map[string]interface{}{}
		if err := rows.MapScan(row); err != nil {
			respondWithInternalError(c, "failed to scan rows", err)
			return
		}
		data = append(data, row)
	}

	if err := rows.Err(); err != nil {
		respondWithInternalError(c, "failed to read rows", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"table":  table,
		"limit":  limit,
		"offset": offset,
		"count":  len(data),
		"data":   data,
	})
}
