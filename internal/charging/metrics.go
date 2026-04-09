package charging

import (
	"sync/atomic"
	"time"
)

type Metrics struct {
	startRequests      atomic.Uint64
	startAccepted      atomic.Uint64
	stopRequests       atomic.Uint64
	stopAccepted       atomic.Uint64
	webhookDuplicates  atomic.Uint64
	activeChargeMillis atomic.Int64
}

func NewMetrics() *Metrics {
	return &Metrics{}
}

func (m *Metrics) RecordStartRequest(accepted bool) {
	m.startRequests.Add(1)
	if accepted {
		m.startAccepted.Add(1)
	}
}

func (m *Metrics) RecordStopRequest(accepted bool) {
	m.stopRequests.Add(1)
	if accepted {
		m.stopAccepted.Add(1)
	}
}

func (m *Metrics) RecordWebhookDuplicate() {
	m.webhookDuplicates.Add(1)
}

func (m *Metrics) RecordChargingDuration(start, end time.Time) {
	if end.After(start) {
		m.activeChargeMillis.Add(end.Sub(start).Milliseconds())
	}
}

type MetricsSnapshot struct {
	StartRequests      uint64 `json:"start_requests"`
	StartAccepted      uint64 `json:"start_accepted"`
	StopRequests       uint64 `json:"stop_requests"`
	StopAccepted       uint64 `json:"stop_accepted"`
	WebhookDuplicates  uint64 `json:"webhook_duplicates"`
	ChargingDurationMs int64  `json:"charging_duration_ms"`
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		StartRequests:      m.startRequests.Load(),
		StartAccepted:      m.startAccepted.Load(),
		StopRequests:       m.stopRequests.Load(),
		StopAccepted:       m.stopAccepted.Load(),
		WebhookDuplicates:  m.webhookDuplicates.Load(),
		ChargingDurationMs: m.activeChargeMillis.Load(),
	}
}
