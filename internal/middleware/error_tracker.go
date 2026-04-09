package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

const maxLoggedBodyBytes = 8192

type bodyCaptureWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *bodyCaptureWriter) Write(data []byte) (int, error) {
	if w.body.Len() < maxLoggedBodyBytes {
		remaining := maxLoggedBodyBytes - w.body.Len()
		if len(data) > remaining {
			w.body.Write(data[:remaining])
		} else {
			w.body.Write(data)
		}
	}
	return w.ResponseWriter.Write(data)
}

func ErrorTracker(db *sqlx.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestBody := captureRequestBody(c.Request)
		writer := &bodyCaptureWriter{ResponseWriter: c.Writer}
		c.Writer = writer

		startedAt := time.Now().UTC()

		defer func() {
			if rec := recover(); rec != nil {
				statusCode := writer.Status()
				if statusCode < http.StatusInternalServerError {
					statusCode = http.StatusInternalServerError
				}

				saveErrorLog(db, errorLogInput{
					RequestID:    requestIDFromContext(c),
					Method:       c.Request.Method,
					Path:         c.Request.URL.Path,
					Route:        c.FullPath(),
					Query:        c.Request.URL.RawQuery,
					StatusCode:   statusCode,
					ErrorMessage: fmt.Sprintf("panic: %v", rec),
					RequestBody:  requestBody,
					ResponseBody: truncateString(writer.body.String(), maxLoggedBodyBytes),
					UserID:       contextString(c, "user_id"),
					ClientIP:     c.ClientIP(),
					UserAgent:    c.Request.UserAgent(),
					StackTrace:   truncateString(string(debug.Stack()), 32000),
					OccurredAt:   startedAt,
				})
				panic(rec)
			}

			statusCode := writer.Status()
			if statusCode < http.StatusBadRequest && len(c.Errors) == 0 {
				return
			}

			saveErrorLog(db, errorLogInput{
				RequestID:    requestIDFromContext(c),
				Method:       c.Request.Method,
				Path:         c.Request.URL.Path,
				Route:        c.FullPath(),
				Query:        c.Request.URL.RawQuery,
				StatusCode:   statusCode,
				ErrorMessage: errorMessageFromContext(c, statusCode, writer.body.String()),
				RequestBody:  requestBody,
				ResponseBody: truncateString(writer.body.String(), maxLoggedBodyBytes),
				UserID:       contextString(c, "user_id"),
				ClientIP:     c.ClientIP(),
				UserAgent:    c.Request.UserAgent(),
				StackTrace:   "",
				OccurredAt:   startedAt,
			})
		}()

		c.Next()
	}
}

type errorLogInput struct {
	RequestID    string
	Method       string
	Path         string
	Route        string
	Query        string
	StatusCode   int
	ErrorMessage string
	RequestBody  string
	ResponseBody string
	UserID       string
	ClientIP     string
	UserAgent    string
	StackTrace   string
	OccurredAt   time.Time
}

func saveErrorLog(db *sqlx.DB, input errorLogInput) {
	const query = `
		INSERT INTO error_logs (
			request_id, method, path, route, query_string, status_code, error_message,
			request_body, response_body, user_id, client_ip, user_agent, stack_trace, occurred_at
		) VALUES (
			NULLIF($1, ''), $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, NULLIF($7, ''),
			NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''), $14
		)
	`

	if _, err := db.Exec(
		query,
		input.RequestID,
		input.Method,
		input.Path,
		input.Route,
		input.Query,
		input.StatusCode,
		input.ErrorMessage,
		input.RequestBody,
		input.ResponseBody,
		input.UserID,
		input.ClientIP,
		input.UserAgent,
		input.StackTrace,
		input.OccurredAt,
	); err != nil {
		log.Printf("error tracker: failed to persist error log: %v", err)
	}
}

func captureRequestBody(r *http.Request) string {
	if r == nil || r.Body == nil {
		return ""
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxLoggedBodyBytes+1))
	if err != nil {
		return fmt.Sprintf("[unreadable request body: %v]", err)
	}

	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return truncateString(string(bodyBytes), maxLoggedBodyBytes)
}

func errorMessageFromContext(c *gin.Context, statusCode int, responseBody string) string {
	sections := []string{
		fmt.Sprintf("status_code=%d", statusCode),
	}

	if len(c.Errors) > 0 {
		errorLines := make([]string, 0, len(c.Errors))
		for _, err := range c.Errors {
			if err == nil {
				continue
			}
			errorLines = append(errorLines, err.Error())
		}
		if len(errorLines) > 0 {
			sections = append(sections, "gin_errors:\n- "+strings.Join(errorLines, "\n- "))
		}
	}

	if responseError := extractResponseError(responseBody); responseError != "" {
		sections = append(sections, "response_error="+responseError)
	}

	responseBody = strings.TrimSpace(responseBody)
	if responseBody != "" {
		sections = append(sections, "response_body="+responseBody)
	}

	if len(sections) == 1 {
		sections = append(sections, "response_body=<empty>")
	}

	return truncateString(strings.Join(sections, "\n"), 4000)
}

func extractResponseError(responseBody string) string {
	responseBody = strings.TrimSpace(responseBody)
	if responseBody == "" {
		return ""
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(responseBody), &payload); err != nil {
		return ""
	}

	keys := []string{"error", "message", "details"}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", key, value))
	}

	if len(parts) > 0 {
		return strings.Join(parts, ", ")
	}

	remainingKeys := make([]string, 0, len(payload))
	for key := range payload {
		remainingKeys = append(remainingKeys, key)
	}
	sort.Strings(remainingKeys)

	for _, key := range remainingKeys {
		if payload[key] == nil {
			continue
		}
		return fmt.Sprintf("%s=%v", key, payload[key])
	}

	return ""
}

func requestIDFromContext(c *gin.Context) string {
	requestID := c.GetHeader("X-Request-ID")
	if requestID == "" {
		requestID = c.Writer.Header().Get("X-Request-ID")
	}
	return requestID
}

func contextString(c *gin.Context, key string) string {
	value, ok := c.Get(key)
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func truncateString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}
