package browser_runtime

import (
	"time"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

// Perception Layer answers: "What does the world look like now?"
// Action runtimes execute commands; observation engines collect verified state.
type perceptionLayer struct {
	browser  *browserObservationEngine
	desktop  *desktopObservationEngine
	files    *fileObservationEngine
	terminal *terminalObservationEngine
	vision   *visionObservationEngine
	audio    *audioObservationEngine
}

func newPerceptionLayer(home string, browserRuntime *browserRuntime) *perceptionLayer {
	return &perceptionLayer{
		browser: &browserObservationEngine{
			home:         home,
			runtime:      browserRuntime,
			orchestrator: perception.NewOrchestrator(perception.DefaultBudget()),
			ocr:          newBrowserOCRProvider(home),
		},
		desktop:  &desktopObservationEngine{},
		files:    &fileObservationEngine{},
		terminal: &terminalObservationEngine{},
		vision:   &visionObservationEngine{},
		audio:    &audioObservationEngine{},
	}
}

func (p *perceptionLayer) ObserveBrowser(request browserExecuteRequest, observation map[string]any, run browserCommandRunner, sessionArgs []string) map[string]any {
	if p == nil || p.browser == nil {
		return observation
	}
	return p.browser.Observe(request, observation, run, sessionArgs)
}

func (p *perceptionLayer) ClearBrowserSession(sessionID string) {
	if p == nil || p.browser == nil || p.browser.orchestrator == nil {
		return
	}
	p.browser.orchestrator.ClearSession(sessionID)
}

type browserObservationEngine struct {
	home         string
	runtime      *browserRuntime
	orchestrator *perception.Orchestrator
	ocr          *browserOCRProvider
}

func (e *browserObservationEngine) Observe(request browserExecuteRequest, observation map[string]any, run browserCommandRunner, sessionArgs []string) map[string]any {
	if e == nil || e.runtime == nil {
		return observation
	}
	decorated := e.runtime.DecorateObservation(request, observation, run, sessionArgs)
	if perception.NeedsSpatialEvidence(perceptionRequest(request), decorated) {
		decorated["element_boxes"] = probeBrowserElementBoxes(
			decorated, request, run, sessionArgs, perception.DefaultBudget().MaxSpatialRefs,
		)
	}
	if e.orchestrator == nil {
		e.orchestrator = perception.NewOrchestrator(perception.DefaultBudget())
	}
	return e.orchestrator.Observe(perceptionRequest(request), decorated, perception.Providers{
		Capture: func(capture perception.CaptureRequest) map[string]any {
			captureRequest := request
			captureRequest.Arguments = clonePerceptionArguments(request.Arguments)
			captureRequest.Arguments["perception_capture"] = true
			captureRequest.Arguments["screenshot_scope"] = capture.Scope
			captureRequest.Arguments["screenshot_reason"] = capture.Reason
			captureRequest.Arguments["screenshot_annotate"] = capture.Annotate
			if capture.Ref != "" {
				captureRequest.Arguments["ref"] = capture.Ref
			}
			return e.runtime.CaptureScreenshot(captureRequest, run, sessionArgs)
		},
		OCR: e.extractOCR,
	})
}

func perceptionRequest(request browserExecuteRequest) perception.Request {
	return perception.Request{
		RequestID: request.RequestID,
		SessionID: request.SessionID,
		Action:    request.Action,
		Arguments: request.Arguments,
	}
}

func (e *browserObservationEngine) extractOCR(request perception.OCRRequest) map[string]any {
	if e == nil || e.ocr == nil {
		return map[string]any{"available": false, "reason": "ocr_provider_not_available"}
	}
	return e.ocr.Extract(request)
}

func clonePerceptionArguments(arguments map[string]any) map[string]any {
	result := make(map[string]any, len(arguments)+3)
	for key, value := range arguments {
		result[key] = value
	}
	return result
}

type desktopObservationEngine struct{}
type fileObservationEngine struct{}
type terminalObservationEngine struct{}
type visionObservationEngine struct{}
type audioObservationEngine struct{}

type perceptionObservation struct {
	Kind       string         `json:"kind"`
	ObservedAt time.Time      `json:"observed_at"`
	State      map[string]any `json:"state,omitempty"`
}
