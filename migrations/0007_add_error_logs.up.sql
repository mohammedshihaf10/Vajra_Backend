CREATE TABLE error_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id      VARCHAR(100),
    method          VARCHAR(10) NOT NULL,
    path            TEXT NOT NULL,
    route           TEXT,
    query_string    TEXT,
    status_code     INTEGER NOT NULL,
    error_message   TEXT,
    request_body    TEXT,
    response_body   TEXT,
    user_id         VARCHAR(100),
    client_ip       VARCHAR(100),
    user_agent      TEXT,
    stack_trace     TEXT,
    occurred_at     TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX error_logs_occurred_at_idx ON error_logs (occurred_at DESC);
CREATE INDEX error_logs_status_code_idx ON error_logs (status_code);
CREATE INDEX error_logs_route_idx ON error_logs (route);
