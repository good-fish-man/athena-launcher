package perception

import (
	"sync"
	"time"
)

const schemaVersion = "athena.perception.v6"

// Request contains only the action context needed by local perception policy.
// It deliberately has no dependency on the browser executor package.
type Request struct {
	RequestID string
	SessionID string
	Action    string
	Arguments map[string]any
}

type Budget struct {
	MaxElements      int `json:"max_elements"`
	MaxContentChars  int `json:"max_content_chars"`
	MaxSnapshotChars int `json:"max_snapshot_chars"`
	MaxSpatialRefs   int `json:"max_spatial_refs"`
	MaxOCRChars      int `json:"max_ocr_chars"`
	MaxCaptures      int `json:"max_captures"`
}

func DefaultBudget() Budget {
	return Budget{
		MaxElements:      30,
		MaxContentChars:  6000,
		MaxSnapshotChars: 12000,
		MaxSpatialRefs:   8,
		MaxOCRChars:      4000,
		MaxCaptures:      1,
	}
}

type CaptureRequest struct {
	Scope    string `json:"scope"`
	Reason   string `json:"reason"`
	Ref      string `json:"ref,omitempty"`
	Annotate bool   `json:"annotate,omitempty"`
}

type CaptureFunc func(CaptureRequest) map[string]any

type OCRRequest struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
	MaxChars int    `json:"max_chars"`
}

type OCRFunc func(OCRRequest) map[string]any

type Providers struct {
	Capture CaptureFunc
	OCR     OCRFunc
}

type Classification struct {
	Type       string   `json:"type"`
	Confidence float64  `json:"confidence"`
	Signals    []string `json:"signals,omitempty"`
}

type IntentSignals struct {
	Visual             bool     `json:"visual"`
	Spatial            bool     `json:"spatial"`
	OCR                bool     `json:"ocr"`
	ExplicitScreenshot bool     `json:"explicit_screenshot"`
	ScreenshotDisabled bool     `json:"screenshot_disabled"`
	Keywords           []string `json:"keywords,omitempty"`
}

type CaptureDecision struct {
	Capture bool   `json:"capture"`
	Scope   string `json:"scope"`
	Reason  string `json:"reason"`
	Ref     string `json:"ref,omitempty"`
}

type AdaptivePolicy struct {
	Level                 int      `json:"level"`
	Profile               string   `json:"profile"`
	Reasons               []string `json:"reasons"`
	Budget                Budget   `json:"budget"`
	MultimodalEvidenceDue bool     `json:"multimodal_evidence_due"`
}

type RecoveryPlan struct {
	Attempt            int      `json:"attempt"`
	Strategy           string   `json:"strategy"`
	Reason             string   `json:"reason"`
	RepeatedAction     int      `json:"repeated_action"`
	CircuitOpen        bool     `json:"circuit_open"`
	BackoffMS          int      `json:"backoff_ms,omitempty"`
	RecommendedActions []string `json:"recommended_actions,omitempty"`
}

type Element struct {
	Ref      string       `json:"ref"`
	Role     string       `json:"role,omitempty"`
	Name     string       `json:"name,omitempty"`
	Label    string       `json:"label"`
	Focused  bool         `json:"focused,omitempty"`
	Disabled bool         `json:"disabled,omitempty"`
	Box      *BoundingBox `json:"box,omitempty"`
	Score    int          `json:"-"`
	Order    int          `json:"-"`
}

type BoundingBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type semanticResult struct {
	Metadata      map[string]any `json:"metadata"`
	Accessibility map[string]any `json:"accessibility"`
	ARIA          map[string]any `json:"aria"`
	FocusedDOM    map[string]any `json:"focused_dom"`
	Elements      []Element      `json:"-"`
	Content       string         `json:"-"`
	Snapshot      string         `json:"-"`
}

type Orchestrator struct {
	budget   Budget
	now      func() time.Time
	mu       sync.Mutex
	sessions map[string]*sessionBaseline
}

func NewOrchestrator(budget Budget) *Orchestrator {
	budget = normalizeBudget(budget)
	return &Orchestrator{budget: budget, now: time.Now, sessions: make(map[string]*sessionBaseline)}
}

func normalizeBudget(budget Budget) Budget {
	defaults := DefaultBudget()
	if budget.MaxElements <= 0 {
		budget.MaxElements = defaults.MaxElements
	}
	if budget.MaxContentChars <= 0 {
		budget.MaxContentChars = defaults.MaxContentChars
	}
	if budget.MaxSnapshotChars <= 0 {
		budget.MaxSnapshotChars = defaults.MaxSnapshotChars
	}
	if budget.MaxSpatialRefs <= 0 {
		budget.MaxSpatialRefs = defaults.MaxSpatialRefs
	}
	if budget.MaxOCRChars <= 0 {
		budget.MaxOCRChars = defaults.MaxOCRChars
	}
	if budget.MaxCaptures <= 0 {
		budget.MaxCaptures = defaults.MaxCaptures
	}
	return budget
}
