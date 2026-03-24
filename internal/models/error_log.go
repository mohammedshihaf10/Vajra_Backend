package models

type ErrorLog struct {
	ID           string `db:"id" json:"id"`
	RequestID    string `db:"request_id" json:"request_id"`
	Method       string `db:"method" json:"method"`
	Path         string `db:"path" json:"path"`
	Route        string `db:"route" json:"route"`
	Query        string `db:"query_string" json:"query_string"`
	StatusCode   int    `db:"status_code" json:"status_code"`
	ErrorMessage string `db:"error_message" json:"error_message"`
	RequestBody  string `db:"request_body" json:"request_body"`
	ResponseBody string `db:"response_body" json:"response_body"`
	UserID       string `db:"user_id" json:"user_id"`
	ClientIP     string `db:"client_ip" json:"client_ip"`
	UserAgent    string `db:"user_agent" json:"user_agent"`
	StackTrace   string `db:"stack_trace" json:"stack_trace"`
	OccurredAt   string `db:"occurred_at" json:"occurred_at"`
}
