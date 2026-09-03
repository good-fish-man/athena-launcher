package browser_runtime

import (
	"athena-launcher/internal/runtime-system/browser-runtime/browser"
	statepkg "athena-launcher/internal/state"
	"context"
	"errors"
	"fmt"
	"sort"
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
	Shutdown(context.Context) error
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
	SemanticTrace        map[string]any
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

func NewControllerWithPersistence(home, dataDir, encryptionKey string) Controller {
	controller := newBrowserController(home)
	controller.dataDir = dataDir
	controller.encryptionKey = encryptionKey
	return controller
}

func (b *browserController) RunAction(ctx context.Context, request Request) (map[string]any, error) {
	return b.runAction(ctx, request)
}

func (b *browserController) RunTask(ctx context.Context, request TaskRequest) (map[string]any, error) {
	return b.runTask(ctx, browserTaskRequest{
		RequestID: request.RequestID, SessionID: request.SessionID, Goal: request.Goal,
		Target: request.Target, Query: request.Query, ContextualMediaTitle: request.ContextualMediaTitle,
		SemanticTrace: request.SemanticTrace,
		Progress:      request.Progress,
	})
}

func (b *browserController) ManageAutomation(ctx context.Context, request AutomationRequest) (map[string]any, error) {
	if b.automation == nil {
		b.automation = newBrowserAutomationEngine(b.home)
		b.automation.bind(b)
	}
	return b.automation.Manage(ctx, request)
}

// Shutdown releases every browser session owned by this controller. An
// auto-connected Chrome belongs to the user, so only Athena's local session
// state is released in that mode.
func (b *browserController) Shutdown(ctx context.Context) error {
	if b == nil || b.runtime == nil {
		return nil
	}
	snapshot := b.runtime.Snapshot()
	sessionIDs := make([]string, 0, len(snapshot.Sessions))
	for sessionID, session := range snapshot.Sessions {
		if !session.Closed {
			sessionIDs = append(sessionIDs, sessionID)
		}
	}
	sort.Strings(sessionIDs)
	closeManagedBrowser := browserAuthMode(b.home) != browserAuthModeAutoConnect
	var failures []error
	for _, sessionID := range sessionIDs {
		if closeManagedBrowser {
			_, err := b.runAction(ctx, browserExecuteRequest{
				SessionID: sessionID,
				Action:    "close",
				Arguments: map[string]any{},
			})
			if err != nil {
				failures = append(failures, fmt.Errorf("close managed browser session %s: %w", sessionID, err))
			}
		}
		b.CloseSession(sessionID)
	}
	return errors.Join(failures...)
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
