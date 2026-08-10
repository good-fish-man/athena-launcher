package browser_runtime

import "testing"

func TestBrowserTaskPlannerCreatesBoundedContinuationPlan(t *testing.T) {
	planning := newBrowserTaskPlanner().Plan(browserTaskRequest{
		SessionID: "athena-0123456789abcdef0123456789abcdef",
		Goal:      "play the second video",
	})
	if planning.Trace.Schema != browserTaskPlannerSchema || planning.Trace.Strategy != "continue_current_session" {
		t.Fatalf("trace = %#v", planning.Trace)
	}
	if planning.Task.ResultOrdinal != 2 || !planning.Task.PlayResult || planning.Trace.MaxActions <= 0 {
		t.Fatalf("task = %#v trace=%#v", planning.Task, planning.Trace)
	}
}

func TestBrowserTaskExecutionBudgetStopsRunawayActions(t *testing.T) {
	budget := newBrowserTaskExecutionBudget(2)
	if err := budget.consume("navigate"); err != nil {
		t.Fatal(err)
	}
	if err := budget.consume("extract"); err != nil {
		t.Fatal(err)
	}
	if err := budget.consume("click"); err == nil {
		t.Fatal("execution budget did not stop a third action")
	}
	snapshot := budget.snapshot()
	if snapshot["used_actions"] != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestBrowserTaskPlannerKeepsMediaControlInCurrentSession(t *testing.T) {
	planning := newBrowserTaskPlanner().Plan(browserTaskRequest{
		SessionID: "athena-0123456789abcdef0123456789abcdef",
		Goal:      "Quite current video",
	})
	if planning.Task.MediaControl != "leave" || planning.Trace.Strategy != "control_current_media" || !planning.Trace.Signals["media_control"] {
		t.Fatalf("media control plan = %#v", planning)
	}
}
