package browser_runtime

import (
	"fmt"
	"strings"
	"sync"
)

const browserTaskPlannerSchema = "athena.browser.task-plan.v3"

type browserTaskPlanningTrace struct {
	Schema      string          `json:"schema"`
	Strategy    string          `json:"strategy"`
	Intent      string          `json:"intent"`
	Signals     map[string]bool `json:"signals"`
	Constraints []string        `json:"constraints"`
	MaxActions  int             `json:"max_actions"`
}

type browserTaskPlanningResult struct {
	Task  inferredBrowserTask
	Trace browserTaskPlanningTrace
}

type browserTaskPlanner struct{}

func newBrowserTaskPlanner() *browserTaskPlanner { return &browserTaskPlanner{} }

func (browserTaskPlanner) Plan(request browserTaskRequest) browserTaskPlanningResult {
	task := inferBrowserTask(request.Goal, request.Target, request.Query)
	if request.ContextualMediaTitle {
		task = inferContextualMediaTask(request.Goal)
	}
	signals := map[string]bool{
		"continue_session": strings.TrimSpace(request.SessionID) != "",
		"media_control":    strings.TrimSpace(task.MediaControl) != "",
		"search":           strings.TrimSpace(task.Query) != "",
		"select":           strings.TrimSpace(task.Selection) != "",
		"open_result":      task.ResultOrdinal > 0 || task.OpenFirstResult,
		"playback":         task.PlayResult,
		"visual_target":    browserTaskRequiresVisualGrounding(task),
	}
	strategy := "navigate_target"
	switch {
	case task.MediaControl != "":
		strategy = "control_current_media"
	case task.Selection != "":
		strategy = "select_current_page"
	case task.Target == "" && request.SessionID != "":
		strategy = "continue_current_session"
	case task.Target != "" && taskTargetURL(task.Target) == "":
		strategy = "resolve_target_then_interact"
	case task.Query != "":
		strategy = "navigate_search_then_resolve"
	}
	return browserTaskPlanningResult{
		Task: task,
		Trace: browserTaskPlanningTrace{
			Schema: browserTaskPlannerSchema, Strategy: strategy, Intent: task.Intent, Signals: signals,
			Constraints: []string{
				"semantic_targets_only", "no_model_selectors_or_coordinates", "bounded_reobserve_and_retry",
				"preserve_session_and_tab_context", "verify_every_interaction", "require_hitl_for_sensitive_actions",
			},
			MaxActions: 18,
		},
	}
}

type browserTaskExecutionBudget struct {
	mu      sync.Mutex
	max     int
	used    int
	actions []string
}

func newBrowserTaskExecutionBudget(maxActions int) *browserTaskExecutionBudget {
	if maxActions <= 0 {
		maxActions = 18
	}
	return &browserTaskExecutionBudget{max: maxActions}
}

func (b *browserTaskExecutionBudget) consume(action string) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.max {
		return fmt.Errorf("browser task exceeded its %d-action execution budget", b.max)
	}
	b.used++
	b.actions = append(b.actions, strings.TrimSpace(action))
	return nil
}

func (b *browserTaskExecutionBudget) snapshot() map[string]any {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]any{
		"max_actions": b.max, "used_actions": b.used, "actions": append([]string(nil), b.actions...),
	}
}
