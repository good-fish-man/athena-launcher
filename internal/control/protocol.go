// Package control defines the Athena device Action/Observation wire protocol.
package control

import "time"

const Protocol = "athena.agent.v3"

// Attachment carries bounded device evidence to Runtime Client. Data is only
// present on the WebSocket hop and must not be persisted or echoed to the UI.
type Attachment struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Encoding string `json:"encoding"`
	Data     string `json:"data,omitempty"`
	Purpose  string `json:"purpose,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type Policy struct {
	Risk     string `json:"risk"`
	Decision string `json:"decision"`
}

type Action struct {
	Protocol       string         `json:"protocol"`
	Type           string         `json:"type"`
	TaskID         string         `json:"task_id"`
	ActionID       string         `json:"action_id"`
	SessionID      string         `json:"session_id"`
	Sequence       int64          `json:"sequence"`
	IdempotencyKey string         `json:"idempotency_key"`
	Deadline       time.Time      `json:"deadline"`
	Capability     string         `json:"capability"`
	Arguments      map[string]any `json:"arguments"`
	Policy         Policy         `json:"policy"`
}

type Observation struct {
	Protocol    string         `json:"protocol"`
	Type        string         `json:"type"`
	TaskID      string         `json:"task_id"`
	ActionID    string         `json:"action_id"`
	SessionID   string         `json:"session_id,omitempty"`
	Sequence    int64          `json:"sequence"`
	Status      string         `json:"status"`
	ObservedAt  time.Time      `json:"observed_at"`
	State       map[string]any `json:"state,omitempty"`
	Attachments []Attachment   `json:"attachments,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type Progress struct {
	Protocol  string         `json:"protocol"`
	Type      string         `json:"type"`
	TaskID    string         `json:"task_id"`
	ActionID  string         `json:"action_id"`
	SessionID string         `json:"session_id,omitempty"`
	Sequence  int64          `json:"sequence"`
	Stage     string         `json:"stage,omitempty"`
	Message   string         `json:"message,omitempty"`
	Progress  int            `json:"progress,omitempty"`
	Bytes     int64          `json:"bytes,omitempty"`
	Total     int64          `json:"total,omitempty"`
	State     map[string]any `json:"state,omitempty"`
	SentAt    time.Time      `json:"sent_at"`
}

type Cancel struct {
	Protocol string `json:"protocol"`
	Type     string `json:"type"`
	TaskID   string `json:"task_id"`
	ActionID string `json:"action_id"`
	Sequence int64  `json:"sequence"`
	Reason   string `json:"reason"`
}
