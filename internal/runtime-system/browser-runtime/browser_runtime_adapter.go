package browser_runtime

import (
	"athena-launcher/internal/runtime-system/browser-runtime/browser"
	statepkg "athena-launcher/internal/state"
	"context"
)

type browserRuntime = browser.Runtime
type browserExecuteRequest = browser.Request
type browserActionProgress = browser.Progress
type browserCommandRunner = browser.CommandRunner

type Request = browser.Request
type Progress = browser.Progress

type Controller interface {
	RunAction(context.Context, Request) (map[string]any, error)
	RunTask(context.Context, TaskRequest) (map[string]any, error)
	ManageAutomation(context.Context, AutomationRequest) (map[string]any, error)
	ResolveSession(string, bool, bool, string) (string, error)
	CloseSession(string)
	SessionArgs(string) []string
	Capabilities() []string
	Available() bool
}

type TaskRequest struct {
	RequestID            string
	SessionID            string
	Goal                 string
	Target               string
	Query                string
	ContextualMediaTitle bool
	Progress             func(Progress)
}

const (
	AuthModeIsolated    = browserAuthModeIsolated
	AuthModeProfile     = browserAuthModeProfile
	AuthModeAutoConnect = browserAuthModeAutoConnect
)

func SessionArgs(sessionID string) []string { return (&browserController{}).sessionArgs(sessionID) }
func KeyElements(snapshot string, limit int) []map[string]string {
	return browserKeyElements(snapshot, limit)
}
func DetectChallenge(value map[string]any) map[string]any { return detectBrowserChallenge(value) }
func ExecutableFileExists(path string) bool               { return executableFileExists(path) }
func IsValidSessionID(value string) bool                  { return browserSessionPattern.MatchString(value) }

func NewController(home string) Controller { return newBrowserController(home) }

func (b *browserController) RunAction(ctx context.Context, request Request) (map[string]any, error) {
	return b.runAction(ctx, request)
}

func (b *browserController) RunTask(ctx context.Context, request TaskRequest) (map[string]any, error) {
	return b.runTask(ctx, browserTaskRequest{
		RequestID: request.RequestID, SessionID: request.SessionID, Goal: request.Goal,
		Target: request.Target, Query: request.Query, ContextualMediaTitle: request.ContextualMediaTitle,
		Progress: request.Progress,
	})
}

func (b *browserController) ManageAutomation(ctx context.Context, request AutomationRequest) (map[string]any, error) {
	if b.automation == nil {
		b.automation = newBrowserAutomationEngine(b.home)
		b.automation.bind(b)
	}
	return b.automation.Manage(ctx, request)
}

func (b *browserController) ResolveSession(requested string, create bool, forceNew bool, targetKey string) (string, error) {
	return b.runtime.ResolveSession(requested, create, forceNew, targetKey)
}

func (b *browserController) CloseSession(sessionID string) {
	if b.automation != nil {
		b.automation.CloseSession(sessionID)
	}
	b.runtime.CloseSession(sessionID)
	b.sessionLocks.Delete(sessionID)
	if b.perception != nil {
		b.perception.ClearBrowserSession(sessionID)
	}
}
func (b *browserController) SessionArgs(sessionID string) []string { return b.sessionArgs(sessionID) }
func (b *browserController) Capabilities() []string                { return b.capabilities() }
func (b *browserController) Available() bool                       { return b.available() }

type SettingsRequest = browserSettingsRequest
type SettingsResponse = browserSettingsResponse

func SettingsFromState(home string, state *statepkg.State) SettingsResponse {
	return browserSettingsFromState(home, state)
}

func ApplySettings(state *statepkg.State, request SettingsRequest) error {
	return applyBrowserSettings(state, request)
}

func newBrowserRuntime(home string) *browserRuntime {
	return browser.NewRuntime(home, browser.Options{
		AuthMode:       func() string { return browserAuthMode(home) },
		ProfilePath:    func() string { return browserProfile(home) },
		ExecutablePath: preferredBrowserExecutable,
	})
}
