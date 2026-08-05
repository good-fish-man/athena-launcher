package main

import "time"

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
		browser:  &browserObservationEngine{home: home, runtime: browserRuntime},
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

func (p *perceptionLayer) CaptureBrowserScreenshot(request browserExecuteRequest, run browserCommandRunner, sessionArgs []string) map[string]any {
	if p == nil || p.browser == nil {
		return nil
	}
	return p.browser.CaptureScreenshot(request, run, sessionArgs)
}

type browserObservationEngine struct {
	home    string
	runtime *browserRuntime
}

func (e *browserObservationEngine) Observe(request browserExecuteRequest, observation map[string]any, run browserCommandRunner, sessionArgs []string) map[string]any {
	if e == nil || e.runtime == nil {
		return observation
	}
	return e.runtime.decorateObservation(request, observation, run, sessionArgs)
}

func (e *browserObservationEngine) CaptureScreenshot(request browserExecuteRequest, run browserCommandRunner, sessionArgs []string) map[string]any {
	if e == nil || e.runtime == nil {
		return nil
	}
	return e.runtime.captureScreenshot(request, run, sessionArgs)
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
