package browser

import "time"

// Request describes one browser action without coupling the browser runtime to
// the desktop transport or UI implementation.
type Request struct {
	RequestID    string         `json:"request_id"`
	SessionID    string         `json:"session_id"`
	Action       string         `json:"action"`
	Arguments    map[string]any `json:"arguments"`
	RiskLevel    string         `json:"risk_level"`
	Decision     string         `json:"decision"`
	UserTakeover bool           `json:"user_takeover"`
	Approved     bool           `json:"approved"`
	Progress     func(Progress) `json:"-"`
}

type Progress struct {
	Stage    string         `json:"stage,omitempty"`
	Message  string         `json:"message,omitempty"`
	Progress int            `json:"progress,omitempty"`
	Bytes    int64          `json:"bytes,omitempty"`
	Total    int64          `json:"total,omitempty"`
	State    map[string]any `json:"state,omitempty"`
}

type ProfileInfo struct {
	Mode string `json:"mode"`
	Path string `json:"path,omitempty"`
}

func (request Request) EmitProgress(progress Progress) {
	if request.Progress == nil {
		return
	}
	if progress.Progress < 0 {
		progress.Progress = 0
	}
	if progress.Progress > 100 {
		progress.Progress = 100
	}
	request.Progress(progress)
}

type browserProfileInfo = ProfileInfo

func callString(callback func() string) string {
	if callback == nil {
		return ""
	}
	return callback()
}

var _ CommandRunner = func(time.Duration, ...string) (string, error) { return "", nil }
