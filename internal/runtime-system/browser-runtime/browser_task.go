package browser_runtime

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
	"athena-launcher/internal/runtime-system/browser-runtime/siteknowledge"
)

const browserCapabilityHandoffSchema = "athena.capability-handoff.v3"

type browserTaskRequest struct {
	RequestID            string
	SessionID            string
	Goal                 string
	Target               string
	Query                string
	ContextualMediaTitle bool
	SemanticTrace        map[string]any
	Progress             func(browserActionProgress)
	Budget               *browserTaskExecutionBudget
	Trace                *browserInteractionTrace
}

type browserTaskPlan struct {
	Goal                 string                     `json:"goal"`
	Target               string                     `json:"target,omitempty"`
	Query                string                     `json:"query,omitempty"`
	SelectedLabel        string                     `json:"selected_label,omitempty"`
	Intent               string                     `json:"intent"`
	ResolvedURL          string                     `json:"resolved_url,omitempty"`
	SelectedURL          string                     `json:"selected_url,omitempty"`
	CandidateSource      string                     `json:"candidate_source,omitempty"`
	CandidateDiagnostics map[string]any             `json:"candidate_diagnostics,omitempty"`
	Planning             *browserTaskPlanningTrace  `json:"planning,omitempty"`
	Resolution           *browserTargetResolution   `json:"resolution,omitempty"`
	ExecutionBudget      map[string]any             `json:"execution_budget,omitempty"`
	Interactions         []browserInteractionReport `json:"interactions,omitempty"`
	Steps                []string                   `json:"steps,omitempty"`
	Completed            bool                       `json:"completed"`
	Message              string                     `json:"message,omitempty"`
	budget               *browserTaskExecutionBudget
	trace                *browserInteractionTrace
	effectTrace          *browserEffectTrace
}

type browserSemanticElement struct {
	Ref   string
	Label string
	URL   string
	Role  string
	Kind  string
}

func (b *browserController) runTask(ctx context.Context, request browserTaskRequest) (map[string]any, error) {
	goal := strings.TrimSpace(request.Goal)
	if goal == "" {
		return nil, fmt.Errorf("browser task goal is required")
	}
	plan := browserTaskPlan{Goal: goal, Target: strings.TrimSpace(request.Target), Query: strings.TrimSpace(request.Query)}
	if request.SemanticTrace != nil {
		trace, err := newBrowserEffectTrace(request.SemanticTrace, request.RequestID, request.SessionID)
		if err != nil {
			return nil, fmt.Errorf("decode browser effect trace: %w", err)
		}
		plan.effectTrace = trace
	}
	if b.taskPlanner == nil {
		b.taskPlanner = newBrowserTaskPlanner()
	}
	planning := b.taskPlanner.Plan(request)
	task := planning.Task
	plan.Planning = &planning.Trace
	request.Budget = newBrowserTaskExecutionBudget(planning.Trace.MaxActions)
	request.Trace = newBrowserInteractionTrace()
	plan.budget = request.Budget
	plan.trace = request.Trace
	plan.Intent, plan.Target, plan.Query = task.Intent, task.Target, task.Query
	if request.Progress != nil {
		request.Progress(browserActionProgress{Stage: "planning", Progress: 5, Message: "Planning browser task", State: map[string]any{"goal": goal, "target": plan.Target, "query": plan.Query}})
	}
	if task.PageControl != "" {
		if strings.TrimSpace(request.SessionID) == "" {
			return b.finishTask(nil, plan, fmt.Errorf("no active browser session is available for the current page"))
		}
		plan.Steps = append(plan.Steps, task.PageControl+"_current_page")
		state, err := b.runTaskAction(ctx, request, task.PageControl, map[string]any{"snapshot": true}, "Refreshing current browser page", 85)
		if err != nil || browserInterventionRequired(state) {
			return b.finishTask(state, plan, err)
		}
		plan.Completed = true
		plan.Message = "Current browser page refreshed."
		return b.finishTask(state, plan, nil)
	}
	if task.MediaControl != "" {
		if strings.TrimSpace(request.SessionID) == "" {
			return b.finishTask(nil, plan, fmt.Errorf("no active browser session is available for the current media"))
		}
		plan.Steps = append(plan.Steps, task.MediaControl+"_current_media")
		state, err := b.runTaskAction(ctx, request, "extract", map[string]any{"snapshot": true}, "Observing current page media", 25)
		if err != nil || browserInterventionRequired(state) {
			return b.finishTask(state, plan, err)
		}
		beforeURL := browserStringValue(state["url"])
		if task.MediaControl == "leave" {
			state, err = b.runTaskAction(ctx, request, "back", map[string]any{"snapshot": true}, "Leaving the current media page", 85)
			if err == nil && normalizeBrowserPageURL(beforeURL) == normalizeBrowserPageURL(browserStringValue(state["url"])) {
				plan.Steps = append(plan.Steps, "pause_media_without_navigation_history")
				state, err = b.runTaskAction(ctx, request, "pause", map[string]any{"snapshot": true}, "Pausing media because no previous page is available", 90)
				if err == nil {
					plan.Message = "No previous page was available, so the current media was paused."
				}
			}
		} else {
			state, err = b.runTaskAction(ctx, request, "pause", map[string]any{"snapshot": true}, "Pausing current page media", 85)
		}
		if err != nil || browserInterventionRequired(state) {
			return b.finishTask(state, plan, err)
		}
		plan.Completed = true
		if task.MediaControl == "leave" && plan.Message == "" {
			plan.Message = "Left the current media page."
		} else if task.MediaControl == "pause" {
			plan.Message = "Current media playback is paused."
		}
		return b.finishTask(state, plan, nil)
	}
	if task.Selection != "" {
		state, err := b.selectCurrentPageElement(ctx, request, task, &plan)
		if err != nil || browserInterventionRequired(state) {
			return b.finishTask(state, plan, err)
		}
		plan.Completed = true
		plan.Message = "Current page option selected."
		return b.finishTask(state, plan, nil)
	}
	if task.PlayResult && task.Target == "" && task.Query == "" && task.ResultOrdinal == 0 {
		plan.Intent = "play_current_media"
		plan.Steps = append(plan.Steps, "play_current_media")
		state, err := b.runTaskAction(ctx, request, "play", map[string]any{"snapshot": true, "verify_playback": true}, "Starting current page media", 85)
		if err != nil || browserInterventionRequired(state) {
			return b.finishTask(state, plan, err)
		}
		plan.Completed = true
		plan.Message = "Media playback was verified."
		return b.finishTask(state, plan, nil)
	}

	state, err := b.openTaskTarget(ctx, request, task, &plan)
	if err != nil || browserInterventionRequired(state) {
		return b.finishTask(state, plan, err)
	}
	if browserCapabilityHandoffRequired(state) {
		plan.Completed = false
		plan.Message = "Search capability is resolving an exact website URL before browser execution continues."
		plan.Steps = append(plan.Steps, "await_capability_handoff")
		return b.finishTask(state, plan, nil)
	}
	if browserPageSelectionRequired(state) {
		return b.finishTaskForPageSelection(state, plan)
	}
	if task.ContextualMediaTitle {
		task, err = contextualizeMediaTask(task, state)
		if err != nil {
			return b.finishTask(state, plan, err)
		}
		plan.Intent, plan.Target, plan.Query = task.Intent, task.Target, task.Query
		plan.Steps = append(plan.Steps, "interpret_contextual_media_title")
	}
	if task.Target == "" {
		if knowledge, matched := siteknowledge.MatchURL(browserStringValue(state["url"])); matched {
			task.Target = knowledge.DisplayName
			plan.Target = task.Target
		}
	}
	if task.Query != "" {
		state, err = b.searchWithinTaskTarget(ctx, request, task, state, &plan)
		if err != nil || browserInterventionRequired(state) {
			return b.finishTask(state, plan, err)
		}
		if browserPageSelectionRequired(state) {
			return b.finishTaskForPageSelection(state, plan)
		}
	}
	if task.ResultOrdinal > 0 || task.OpenFirstResult {
		state, err = b.openFirstTaskResult(ctx, request, task, state, &plan)
		if err != nil || browserChallengeDetected(state) || browserInterventionRequired(state) {
			return b.finishTask(state, plan, err)
		}
	}
	plan.Completed = true
	plan.Message = "Browser task completed."
	return b.finishTask(state, plan, nil)
}

func browserPageSelectionRequired(state map[string]any) bool {
	model, ok := perception.SemanticPage(state)
	return ok && strings.EqualFold(strings.TrimSpace(model.Type), "profile_selection")
}

func (b *browserController) finishTaskForPageSelection(state map[string]any, plan browserTaskPlan) (map[string]any, error) {
	plan.Completed = false
	plan.Message = "Choose a visible profile or account to continue this browser task."
	plan.Steps = append(plan.Steps, "await_page_selection")
	if state == nil {
		state = make(map[string]any)
	}
	state["continuation_required"] = map[string]any{
		"kind": "page_selection", "reason": "visible_choice_required",
	}
	return b.finishTask(state, plan, nil)
}

func (b *browserController) selectCurrentPageElement(
	ctx context.Context,
	request browserTaskRequest,
	task inferredBrowserTask,
	plan *browserTaskPlan,
) (map[string]any, error) {
	selection := task.Selection
	plan.Steps = append(plan.Steps, "observe_current_page")
	state, err := b.runTaskAction(ctx, request, "extract", map[string]any{"snapshot": true}, "Reading the current browser page", 20)
	if err != nil || browserInterventionRequired(state) {
		return state, err
	}

	candidate, found := b.resolveCurrentPageSelection(state, task, plan)
	for attempt, delay := range []string{"1200", "1800"} {
		if found || browserChallengeDetected(state) || browserInterventionRequired(state) {
			break
		}
		plan.Steps = append(plan.Steps, "wait_for_current_page_options")
		state, err = b.runTaskAction(ctx, request, "wait", map[string]any{"value": delay, "snapshot": true}, "Waiting for page options", 35+attempt*8)
		if err != nil || browserInterventionRequired(state) {
			return state, err
		}
		candidate, found = b.resolveCurrentPageSelection(state, task, plan)
	}
	if !found && plan.Resolution != nil && (plan.Resolution.RequiresVisual || plan.Resolution.RequiresSpatial) {
		plan.Steps = append(plan.Steps, "capture_current_page_target_evidence")
		state, err = b.runTaskAction(ctx, request, "screenshot", map[string]any{
			"screenshot_scope": "viewport", "snapshot": true, "goal": plan.Goal,
			"query": task.GroundingQuery, "target_resolution": true,
		}, "Capturing page evidence for the requested control", 65)
		if err != nil || browserInterventionRequired(state) {
			return state, err
		}
		candidate, found = b.resolveCurrentPageSelection(state, task, plan)
	}
	if !found {
		if plan.Resolution != nil && len(plan.Resolution.Candidates) > 0 {
			return browserTaskResolutionIntervention(state, plan)
		}
		return state, fmt.Errorf("could not safely match %q to a visible option on the current page", selection)
	}

	currentURL := browserStringValue(state["url"])
	plan.SelectedLabel = candidate.Label
	plan.SelectedURL = candidate.URL
	plan.Steps = append(plan.Steps, "select_current_page_option")
	state, err = b.runTaskAction(ctx, request, "click", map[string]any{
		"ref":                          candidate.Ref,
		"target_label":                 plan.SelectedLabel,
		"expected_page_url":            currentURL,
		"wait_for_document_transition": browserCurrentPageSelectionWaitsForTransition(candidate),
		"snapshot":                     true,
	}, "Selecting the current page option", 70)
	if err != nil || browserInterventionRequired(state) {
		return state, err
	}

	plan.Steps = append(plan.Steps, "observe_selected_page")
	return b.runTaskAction(ctx, request, "wait", map[string]any{
		"value": "1200", "snapshot": true,
	}, "Verifying the selected page", 90)
}

func (b *browserController) resolveCurrentPageSelection(
	state map[string]any,
	task inferredBrowserTask,
	plan *browserTaskPlan,
) (browserTargetCandidate, bool) {
	if b.targetResolver == nil {
		b.targetResolver = newBrowserTargetResolver()
	}
	resolutionTask := task
	resolutionTask.Target = ""
	resolutionTask.Query = strings.TrimSpace(task.GroundingQuery)
	if resolutionTask.Query == "" {
		resolutionTask.Query = strings.TrimSpace(task.Selection)
	}
	resolutionTask.ResultOrdinal = 1
	resolutionTask.OpenFirstResult = false
	resolutionTask.PlayResult = false
	resolutionTask.PreferredKinds = nil
	resolution := b.targetResolver.Resolve(state, resolutionTask, 1, "LOW")
	plan.Resolution = &resolution
	if resolution.Decision != browserResolutionExecute || resolution.Selected == nil || !browserRefPattern.MatchString(resolution.Selected.Ref) {
		return browserTargetCandidate{}, false
	}
	return *resolution.Selected, true
}

func browserCurrentPageSelectionWaitsForTransition(candidate browserTargetCandidate) bool {
	return strings.TrimSpace(candidate.URL) != ""
}

type inferredBrowserTask struct {
	Intent               string
	Target               string
	Query                string
	Selection            string
	GroundingQuery       string
	MediaControl         string
	PageControl          string
	OpenFirstResult      bool
	ResultOrdinal        int
	PlayResult           bool
	PreferredKinds       []string
	PreferredRoles       []string
	ContextualMediaTitle bool
}

var browserEmbeddedCurrentPageActionPattern = regexp.MustCompile(`(?is)(?:^|[\s,;:，；：])(?:click|tap|select|choose|press|点击|點擊|选择|選擇|选中|選中|按下)\s+(.+)$`)

func inferContextualMediaTask(goal string) inferredBrowserTask {
	return inferredBrowserTask{
		Intent: "contextual_media_search", Query: strings.TrimSpace(goal), ContextualMediaTitle: true,
	}
}

func contextualizeMediaTask(task inferredBrowserTask, state map[string]any) (inferredBrowserTask, error) {
	currentURL := browserStringValue(state["url"])
	knowledge, knownSite := siteknowledge.MatchURL(currentURL)
	if knownSite && knowledge.MediaContinuation != nil {
		hints := knowledge.MediaContinuation
		task.Target = knowledge.DisplayName
		task.PreferredKinds = append([]string(nil), hints.PreferredKinds...)
		task.OpenFirstResult = hints.OpenFirst
		task.PlayResult = hints.Play
	} else {
		kinds, mediaPage := contextualMediaKinds(state)
		if !mediaPage {
			return task, fmt.Errorf("the current browser page is not a recognized media catalog; include the website or say what action to perform")
		}
		task.PreferredKinds = kinds
		task.OpenFirstResult = true
		task.PlayResult = true
		if knownSite {
			task.Target = knowledge.DisplayName
		}
	}
	if strings.TrimSpace(task.Query) == "" {
		return task, fmt.Errorf("the media title is empty")
	}
	if task.OpenFirstResult {
		task.ResultOrdinal = 1
	}
	if task.PlayResult {
		task.Intent = "search_open_and_play_media"
	} else if task.OpenFirstResult {
		task.Intent = "search_and_open_media"
	} else {
		task.Intent = "search_media"
	}
	return task, nil
}

func contextualMediaKinds(state map[string]any) ([]string, bool) {
	model, ok := perception.SemanticPage(state)
	if !ok {
		return nil, false
	}
	counts := map[string]int{"video": 0, "audio": 0}
	for _, entity := range model.Entities {
		kind := strings.ToLower(strings.TrimSpace(entity.Kind))
		if kind == "video" || kind == "audio" {
			counts[kind]++
		}
	}
	if counts["video"] == 0 && counts["audio"] == 0 {
		if !strings.EqualFold(strings.TrimSpace(model.Type), "media_catalog") {
			return nil, false
		}
		return []string{"video", "audio"}, true
	}
	if counts["audio"] > counts["video"] {
		return []string{"audio", "video"}, true
	}
	return []string{"video", "audio"}, true
}

func inferBrowserTask(goal, target, query string) inferredBrowserTask {
	lowerGoal := strings.ToLower(goal)
	result := inferredBrowserTask{Intent: "open", Target: strings.TrimSpace(target), Query: strings.TrimSpace(query)}
	if control := inferCurrentMediaControl(lowerGoal); control != "" {
		result.Intent = control + "_current_media"
		result.MediaControl = control
		result.Target = ""
		result.Query = ""
		return result
	}
	if browserGoalRequestsCurrentPageRefresh(lowerGoal) {
		result.Intent = "refresh_current_page"
		result.PageControl = "refresh"
		result.Target = ""
		result.Query = ""
		return result
	}
	if selection, grounding := inferCurrentPageSelectionIntent(goal); selection != "" {
		result.Intent = "select_current"
		result.Selection = selection
		result.GroundingQuery = grounding
		result.Target = ""
		result.Query = ""
		return result
	}
	if result.Target == "" {
		result.Target = inferBrowserTarget(goal)
	}
	result.ResultOrdinal = inferBrowserResultOrdinal(lowerGoal)
	result.PlayResult = browserGoalRequestsPlayback(lowerGoal)
	result.PreferredKinds = inferBrowserResultKinds(lowerGoal)
	// An ordinal operation on the current/home feed is navigation, not a
	// search. Models sometimes copy the whole instruction into query.
	if result.ResultOrdinal > 0 && !browserGoalRequestsSearch(lowerGoal) {
		result.Query = ""
	} else if result.Query == "" {
		result.Query = inferBrowserQuery(goal, result.Target)
		if result.Query == "" {
			result.Query = inferImplicitMediaQuery(goal, result.Target, result.PreferredKinds, result.PlayResult)
			if result.Query != "" {
				// A named media item is content, not an unknown website. Keep an
				// independently named site (for example Vimeo), but discard targets
				// that merely repeat the requested title.
				if browserTargetLooksLikeMediaTitle(result.Target, result.Query) {
					result.Target = ""
				}
				result.PlayResult = true
				if result.ResultOrdinal == 0 {
					result.ResultOrdinal = 1
				}
			}
		}
	}
	if result.ResultOrdinal > 0 && browserGoalRequestsMediaOpen(lowerGoal, result.PreferredKinds) {
		result.PlayResult = true
	}
	if result.PlayResult && result.Query != "" && result.ResultOrdinal == 0 {
		result.ResultOrdinal = 1
	}
	if result.Query != "" {
		result.Intent = "search"
	}
	if result.ResultOrdinal > 0 {
		result.OpenFirstResult = result.ResultOrdinal == 1
		result.Intent = "open_result"
	}
	if result.ResultOrdinal == 1 {
		result.OpenFirstResult = true
		result.Intent = "open_first_result"
	}
	if result.Target == "" && result.Query == "" && result.ResultOrdinal == 0 && !result.PlayResult {
		result.Target = goal
	}
	return result
}

func browserGoalRequestsCurrentPageRefresh(goal string) bool {
	return containsBrowserPhrase(strings.ToLower(strings.TrimSpace(goal)), []string{
		"refresh current page", "refresh the current page", "refresh this page",
		"refresh current browser page", "refresh the current browser page", "refresh this browser page",
		"reload current page", "reload the current page", "reload this page",
		"reload current browser page", "reload the current browser page", "reload this browser page",
		"刷新当前页面", "刷新这个页面", "刷新此页面", "重新加载当前页面", "重新加载这个页面",
	})
}

func inferCurrentMediaControl(goal string) string {
	normalized := strings.ToLower(strings.TrimSpace(goal))
	if normalized == "" {
		return ""
	}
	// "quite" is a frequent speech-to-text and typing error for "quit".
	tokens := strings.Fields(normalized)
	for index, token := range tokens {
		if token == "quite" {
			tokens[index] = "quit"
		}
	}
	normalized = strings.Join(tokens, " ")
	media := containsBrowserPhrase(normalized, []string{
		"video", "movie", "film", "media", "audio", "song", "track",
		"视频", "視頻", "影片", "电影", "電影", "媒体", "媒體", "音频", "音頻", "歌曲", "音乐", "音樂",
	})
	current := containsBrowserPhrase(normalized, []string{"current", "this", "当前", "當前", "这个", "這個"})
	if !media || !current {
		return ""
	}
	if containsBrowserPhrase(normalized, []string{"pause", "stop", "暂停", "暫停", "停止", "停止播放"}) {
		return "pause"
	}
	if containsBrowserPhrase(normalized, []string{"quit", "exit", "close", "leave", "退出", "关闭", "關閉", "离开", "離開"}) {
		return "leave"
	}
	return ""
}

func containsBrowserPhrase(value string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

func inferImplicitMediaQuery(goal, target string, preferredKinds []string, playback bool) string {
	mediaIntent := playback
	for _, kind := range preferredKinds {
		if kind == "video" || kind == "audio" {
			mediaIntent = true
			break
		}
	}
	if !mediaIntent {
		return ""
	}

	query := cleanImplicitMediaQuery(goal, target)
	if query == "" && strings.TrimSpace(target) != "" && taskTargetURL(target) == "" {
		query = cleanImplicitMediaQuery(goal, "")
	}
	if isGenericMediaReference(query) {
		return ""
	}
	return query
}

func cleanImplicitMediaQuery(goal, target string) string {
	value := strings.TrimSpace(goal)
	if target != "" {
		value = removeFoldedBrowserPhrase(value, target)
	}
	value = strings.Trim(value, ` "'()[]{}<>:：,，。.;；!?！？`)

	for {
		before := value
		lower := strings.ToLower(value)
		for _, prefix := range []string{
			"could you please ", "would you please ", "could you ", "would you ", "can you ", "please ",
			"help me ", "for me ", "in ", "on ", "from ", "using ", "with ", "and then ", "then ", "and ",
			"open ", "play ", "watch ", "listen to ", "listen ", "start playback of ", "start ",
			"请帮我", "請幫我", "帮我", "幫我", "麻烦帮我", "麻煩幫我", "麻烦", "麻煩", "请", "請", "给我", "給我",
			"在", "从", "從", "用", "通过", "通過", "并且", "並且", "然后", "然後", "并", "並",
			"打开", "打開", "播放", "观看", "觀看", "收听", "收聽", "开始播放", "開始播放",
		} {
			if strings.HasPrefix(lower, prefix) {
				value = strings.TrimSpace(value[len(prefix):])
				value = strings.Trim(value, ` "'()[]{}<>:：,，。.;；!?！？`)
				break
			}
		}
		if value == before {
			break
		}
	}

	for _, suffix := range []string{
		" and play it", " and start playback", " then play it", " for me", " please",
		"并开始播放", "並開始播放", "然后播放", "然後播放", "并播放", "並播放", "并打开", "並打開",
	} {
		if index := strings.Index(strings.ToLower(value), suffix); index > 0 {
			value = strings.TrimSpace(value[:index])
		}
	}

	for {
		before := value
		lower := strings.ToLower(strings.TrimSpace(value))
		for _, suffix := range []string{
			" videos", " video", " songs", " song", " tracks", " track", " audio", " music",
			"的视频", "的視頻", "的影片", "的歌曲", "的音乐", "的音樂", "视频", "視頻", "影片", "歌曲", "音乐", "音樂",
		} {
			if strings.HasSuffix(lower, suffix) {
				value = strings.TrimSpace(value[:len(value)-len(suffix)])
				value = strings.Trim(value, ` "'()[]{}<>:：,，。.;；!?！？`)
				break
			}
		}
		if value == before {
			break
		}
	}
	return strings.TrimSpace(value)
}

func removeFoldedBrowserPhrase(value, phrase string) string {
	lowerValue := strings.ToLower(value)
	lowerPhrase := strings.ToLower(strings.TrimSpace(phrase))
	if lowerPhrase == "" {
		return value
	}
	if index := strings.Index(lowerValue, lowerPhrase); index >= 0 {
		return value[:index] + value[index+len(phrase):]
	}
	return value
}

func isGenericMediaReference(value string) bool {
	normalized := normalizeCurrentPageSelection(value)
	normalized = strings.ReplaceAll(normalized, " ", "")
	if normalized == "" {
		return true
	}
	for _, generic := range []string{
		"current", "this", "that", "it", "one", "some", "corresponding", "selected", "first", "second", "third",
		"当前", "當前", "这个", "這個", "那个", "那個", "它", "对应", "對應", "对应的", "對應的", "选中的", "選中的",
		"第一个", "第一個", "第二个", "第二個", "第三个", "第三個",
	} {
		if normalized == strings.ReplaceAll(normalizeCurrentPageSelection(generic), " ", "") {
			return true
		}
	}
	return false
}

func browserTargetLooksLikeMediaTitle(target, query string) bool {
	if strings.TrimSpace(target) == "" || taskTargetURL(target) != "" {
		return false
	}
	normalize := func(value string) string {
		value = strings.ToLower(normalizeCurrentPageSelection(value))
		for _, marker := range []string{"video", "videos", "song", "songs", "track", "tracks", "audio", "music", "视频", "視頻", "影片", "歌曲", "音乐", "音樂", "的"} {
			value = strings.ReplaceAll(value, marker, "")
		}
		return strings.ReplaceAll(strings.TrimSpace(value), " ", "")
	}
	targetValue, queryValue := normalize(target), normalize(query)
	return targetValue != "" && queryValue != "" && (targetValue == queryValue || strings.Contains(targetValue, queryValue))
}

func browserGoalRequestsMediaOpen(goal string, preferredKinds []string) bool {
	media := false
	for _, kind := range preferredKinds {
		if kind == "video" || kind == "audio" {
			media = true
			break
		}
	}
	if !media {
		return false
	}
	for _, marker := range []string{"open", "watch", "listen", "打开", "打開", "观看", "觀看", "收听", "收聽"} {
		if strings.Contains(goal, marker) {
			return true
		}
	}
	return false
}

func inferCurrentPageSelection(goal string) string {
	selection, _ := inferCurrentPageSelectionIntent(goal)
	return selection
}

func inferCurrentPageSelectionIntent(goal string) (string, string) {
	value := strings.TrimSpace(goal)
	lower := strings.ToLower(value)
	remainder := ""
	for _, prefix := range []string{
		"switch to ", "continue with ", "select ", "choose ", "click ", "tap ", "pick ", "use ",
		"切换到", "切換到", "选择", "選擇", "选中", "選中", "点击", "點擊", "使用",
	} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		remainder = strings.TrimSpace(value[len(prefix):])
		break
	}
	if remainder == "" {
		if !browserGoalScopesCurrentPage(lower) {
			return "", ""
		}
		match := browserEmbeddedCurrentPageActionPattern.FindStringSubmatch(value)
		if len(match) != 2 {
			return "", ""
		}
		remainder = strings.TrimSpace(match[1])
	}

	relation := parseBrowserSpatialRelation(inferredBrowserTask{Query: remainder})
	selection := remainder
	grounding := ""
	if relation != nil && relation.TargetQuery != "" {
		selection = relation.TargetQuery
		modifier := ""
		if relation.Immediate {
			modifier = " immediately"
		}
		grounding = strings.TrimSpace(selection + modifier + " to the " + relation.Direction + " of " + relation.AnchorQuery)
	}
	selection = normalizeBrowserSelectionPhrase(selection)
	if selection == "" {
		return "", ""
	}
	if grounding == "" {
		grounding = selection
	}
	return selection, grounding
}

func browserGoalScopesCurrentPage(goal string) bool {
	for _, marker := range []string{
		"current page", "this page", "current youtube page", "currently open page", "open page",
		"当前页面", "當前頁面", "这个页面", "這個頁面", "本页", "本頁", "当前网页", "當前網頁",
	} {
		if strings.Contains(goal, marker) {
			return true
		}
	}
	return false
}

func normalizeBrowserSelectionPhrase(value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"'()[]{}<>:：,，。.;；!?！？`)
	lower := strings.ToLower(value)
	for _, article := range []string{"the ", "this ", "that ", "当前", "當前", "这个", "這個"} {
		if strings.HasPrefix(lower, article) {
			value = strings.TrimSpace(value[len(article):])
			break
		}
	}
	return strings.Trim(strings.TrimSpace(value), `"'()[]{}<>:：,，。.;；!?！？`)
}

func inferBrowserResultKinds(goal string) []string {
	goal = strings.ToLower(strings.TrimSpace(goal))
	switch {
	case containsBrowserIntentMarker(goal, "repository", "repositories", "repo", "repos", "仓库", "倉庫"):
		return []string{"repository"}
	case containsBrowserIntentMarker(goal, "video", "videos", "vido", "影片", "视频", "視頻"):
		return []string{"video"}
	case containsBrowserIntentMarker(goal, "song", "songs", "track", "tracks", "audio", "music", "歌曲", "音乐", "音樂"):
		return []string{"audio"}
	case containsBrowserIntentMarker(goal, "discussion", "discussions", "thread", "threads", "forum", "post", "posts", "讨论", "討論", "帖子", "论坛", "論壇", "话题", "話題"):
		return []string{"discussion"}
	case containsBrowserIntentMarker(goal, "article", "articles", "story", "stories", "news", "blog", "文章", "新闻", "新聞", "资讯", "資訊"):
		return []string{"article", "content"}
	case containsBrowserIntentMarker(goal, "product", "products", "goods", "商品", "产品", "產品"):
		return []string{"product"}
	default:
		return nil
	}
}

func containsBrowserIntentMarker(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func browserGoalRequestsSearch(goal string) bool {
	for _, marker := range []string{"search", "look up", "find", "搜索", "查询", "查找", "搜一下"} {
		if strings.Contains(goal, marker) {
			return true
		}
	}
	return false
}

func browserGoalRequestsPlayback(goal string) bool {
	for _, marker := range []string{"play", "start playback", "播放", "开始播放"} {
		if strings.Contains(goal, marker) {
			return true
		}
	}
	return false
}

func inferBrowserResultOrdinal(goal string) int {
	for ordinal, markers := range map[int][]string{
		1: {"first", "1st", "第一个", "第1个", "首个", "打开一个"},
		2: {"second", "2nd", "第二个", "第2个"},
		3: {"third", "3rd", "第三个", "第3个"},
	} {
		for _, marker := range markers {
			if strings.Contains(goal, marker) {
				return ordinal
			}
		}
	}
	return 0
}

func inferBrowserTarget(goal string) string {
	if explicit := explicitBrowserURL(goal); explicit != "" {
		return explicit
	}
	if knowledge, matched := siteknowledge.MatchTarget(goal); matched {
		return knowledge.DisplayName
	}
	return inferNamedBrowserTarget(goal)
}

func inferNamedBrowserTarget(goal string) string {
	value := strings.TrimSpace(goal)
	lower := strings.ToLower(value)
	for _, courtesy := range []string{"please ", "could you ", "can you ", "请", "麻烦"} {
		if strings.HasPrefix(lower, courtesy) {
			value = strings.TrimSpace(value[len(courtesy):])
			lower = strings.ToLower(value)
			break
		}
	}
	prefixes := []string{"navigate to ", "browse to ", "go to ", "open ", "visit ", "打开", "访问", "进入"}
	matched := false
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			matched = true
			break
		}
	}
	if !matched || value == "" {
		return ""
	}
	lower = strings.ToLower(value)
	for _, currentPage := range []string{"current page", "this page", "当前页面", "这个页面", "本页"} {
		if strings.Contains(lower, currentPage) {
			return ""
		}
	}
	for _, ordinal := range []string{"the first", "the second", "the third", "first ", "second ", "third ", "第一个", "第二个", "第三个", "第1个", "第2个", "第3个"} {
		if strings.HasPrefix(lower, ordinal) {
			return ""
		}
	}

	cut := len(value)
	for _, marker := range []string{
		" home page", " homepage", " landing page", ",", "，", "。", ";", "；",
		" and ", " then ", " search ", " play ", " open the ",
		"首页", "主页", "并", "然后", "搜索", "播放", "打开第",
	} {
		if index := strings.Index(strings.ToLower(value), marker); index >= 0 && index < cut {
			cut = index
		}
	}
	value = strings.Trim(strings.TrimSpace(value[:cut]), `"'()[]{}<>:：,，。.;；!?！？`)
	if value == "" || len([]rune(value)) > 80 {
		return ""
	}
	return value
}

func explicitBrowserURL(value string) string {
	for _, field := range strings.Fields(value) {
		candidate := strings.Trim(field, `"'()[]{}<>,，。；;!?！？`)
		if strings.HasPrefix(strings.ToLower(candidate), "www.") {
			candidate = "https://" + candidate
		}
		parsed, err := url.Parse(candidate)
		if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" {
			return parsed.String()
		}
	}
	return ""
}

func inferBrowserQuery(goal, target string) string {
	text := strings.TrimSpace(goal)
	lower := strings.ToLower(text)
	for _, marker := range []string{"search for", "search", "look up", "find", "搜索", "查询", "查找", "搜一下"} {
		if index := strings.Index(lower, marker); index >= 0 {
			return cleanupBrowserQuery(text[index+len(marker):], target)
		}
	}
	return ""
}

func cleanupBrowserQuery(value, target string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, " ：:,，。.;；")
	for {
		before := value
		if trimmed, ok := trimBrowserQueryTargetPrefix(value, target); ok {
			value = strings.Trim(strings.TrimSpace(trimmed), " ：:,，。.;；")
		}
		lower := strings.ToLower(value)
		for _, noise := range []string{"and", "then", "for", "on", "in", "at", "from", "search", "first", "video", "并", "然后", "打开", "搜索", "查询", "第一个", "视频"} {
			if trimmed, ok := trimBrowserQueryNoisePrefix(value, lower, noise); ok {
				value = strings.TrimSpace(trimmed)
				value = strings.Trim(value, " ：:,，。.;；")
				break
			}
		}
		if value == before {
			break
		}
	}
	value = strings.Trim(value, " ：:,，。.;；")
	lower := strings.ToLower(value)
	for _, marker := range []string{" and open", " then open", " open the first", " 打开", " 然后", " 并打开"} {
		if index := strings.Index(lower, marker); index > 0 {
			value = strings.TrimSpace(value[:index])
			break
		}
	}
	value = strings.Trim(value, " ：:,，。.;；")
	if strings.EqualFold(value, target) {
		return ""
	}
	if len([]rune(value)) > 200 {
		return string([]rune(value)[:200])
	}
	return value
}

func trimBrowserQueryNoisePrefix(value, lower, noise string) (string, bool) {
	if !strings.HasPrefix(lower, noise) {
		return value, false
	}
	remainder := value[len(noise):]
	if remainder == "" {
		return "", true
	}
	joinedLanguage := false
	for _, current := range noise {
		if current > 127 {
			joinedLanguage = true
			break
		}
	}
	if !joinedLanguage {
		first := []rune(remainder)[0]
		if first != ' ' && first != '\t' && first != ':' && first != '：' && first != ',' && first != '，' {
			return value, false
		}
	}
	return strings.TrimSpace(remainder), true
}

func trimBrowserQueryTargetPrefix(value, target string) (string, bool) {
	value = strings.TrimSpace(value)
	target = strings.TrimSpace(target)
	if value == "" || target == "" || len(value) < len(target) || !strings.EqualFold(value[:len(target)], target) {
		return value, false
	}
	if len(value) == len(target) {
		return "", true
	}
	remainder := value[len(target):]
	first := []rune(remainder)[0]
	if first != ' ' && first != '\t' && first != ':' && first != '：' && first != ',' && first != '，' {
		return value, false
	}
	return strings.TrimSpace(remainder), true
}

func (b *browserController) openTaskTarget(ctx context.Context, request browserTaskRequest, task inferredBrowserTask, plan *browserTaskPlan) (map[string]any, error) {
	targetURL := taskTargetURL(task.Target)
	if targetURL == "" && task.Target == "" && request.SessionID != "" && (task.Query != "" || task.ResultOrdinal > 0 || task.PlayResult) {
		plan.Steps = append(plan.Steps, "observe_current_page")
		return b.runTaskAction(ctx, request, "extract", map[string]any{"snapshot": true}, "Reading the current browser page", 20)
	}
	if targetURL == "" && task.Target != "" {
		return b.discoverAndOpenTaskTarget(ctx, request, task, plan)
	}
	if targetURL == "" && task.Query != "" {
		return browserTaskCapabilityHandoff(request, task, plan), nil
	}
	if targetURL == "" && request.SessionID != "" && (task.ResultOrdinal > 0 || task.PlayResult) {
		plan.Steps = append(plan.Steps, "observe_current_page")
		return b.runTaskAction(ctx, request, "extract", map[string]any{"snapshot": true}, "Reading the current browser page", 20)
	}
	if targetURL == "" {
		return browserTaskCapabilityHandoff(request, task, plan), nil
	}
	plan.Steps = append(plan.Steps, "open_target")
	return b.runTaskAction(ctx, request, "navigate", map[string]any{
		"url": targetURL, "target": task.Target, "query": task.Query, "headed": true, "snapshot": true, "open_mode": "tab",
	}, "Opening browser target", 20)
}

func (b *browserController) discoverAndOpenTaskTarget(ctx context.Context, request browserTaskRequest, task inferredBrowserTask, plan *browserTaskPlan) (map[string]any, error) {
	return browserTaskCapabilityHandoff(request, task, plan), nil
}

func browserTaskCapabilityHandoff(request browserTaskRequest, task inferredBrowserTask, plan *browserTaskPlan) map[string]any {
	query := strings.TrimSpace(task.Target)
	if query != "" {
		query += " official website"
	} else if strings.TrimSpace(task.Query) != "" {
		query = strings.TrimSpace(task.Query)
	} else {
		query = strings.TrimSpace(plan.Goal)
	}
	plan.Steps = append(plan.Steps, "request_search_capability")
	return map[string]any{
		"schema": browserCapabilityHandoffSchema,
		"capability_handoff": map[string]any{
			"schema": browserCapabilityHandoffSchema,
			"from":   "browser.task", "to": "internet.search", "reason": "exact_url_required", "query": query,
			"session_id": request.SessionID,
			"resume": map[string]any{
				"capability": "browser.task", "goal": plan.Goal, "target": task.Target, "query": task.Query,
				"contextual_media_title": task.ContextualMediaTitle,
			},
		},
		"continuation_required": map[string]any{
			"kind": "capability_handoff", "reason": "exact_url_required", "capability": "internet.search",
		},
	}
}

func browserCapabilityHandoffRequired(state map[string]any) bool {
	if state == nil {
		return false
	}
	_, ok := state["capability_handoff"].(map[string]any)
	return ok
}

func browserDiscoveryRootURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return ""
	}
	parsed.Path = "/"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (b *browserController) searchWithinTaskTarget(ctx context.Context, request browserTaskRequest, task inferredBrowserTask, state map[string]any, plan *browserTaskPlan) (map[string]any, error) {
	if searchURL := knownTaskSearchURL(task); searchURL != "" {
		plan.Steps = append(plan.Steps, "navigate_known_search_results")
		return b.runTaskAction(ctx, request, "navigate", map[string]any{
			"url": searchURL, "target": task.Target, "query": task.Query, "snapshot": true, "open_mode": "current",
		}, "Opening typed search results", 65)
	}
	if ref := semanticSearchInputRef(state); ref != "" {
		plan.Steps = append(plan.Steps, "type_query")
		if next, typeErr := b.runTaskAction(ctx, request, "type", map[string]any{"ref": ref, "value": task.Query, "snapshot": true}, "Typing search query", 45); typeErr == nil {
			state = next
		} else {
			plan.Steps = append(plan.Steps, "fallback_to_direct_search")
			return b.runTaskAction(ctx, request, "navigate", map[string]any{
				"url": taskSearchURL(task), "target": task.Target, "query": task.Query, "snapshot": true, "open_mode": "tab",
			}, "Opening search results directly", 65)
		}
		plan.Steps = append(plan.Steps, "submit_query")
		state, err := b.runTaskAction(ctx, request, "press", map[string]any{"value": "Enter", "snapshot": true}, "Submitting search query", 60)
		if err != nil {
			return state, err
		}
		plan.Steps = append(plan.Steps, "wait_for_results")
		return b.runTaskAction(ctx, request, "wait", map[string]any{"value": "1500", "snapshot": true}, "Waiting for results", 75)
	}
	if task.Query != "" {
		plan.Steps = append(plan.Steps, "navigate_search_results")
		return b.runTaskAction(ctx, request, "navigate", map[string]any{
			"url": taskSearchURL(task), "target": task.Target, "query": task.Query, "snapshot": true, "open_mode": "current",
		}, "Opening search results", 65)
	}
	return state, nil
}

func (b *browserController) openFirstTaskResult(ctx context.Context, request browserTaskRequest, task inferredBrowserTask, state map[string]any, plan *browserTaskPlan) (map[string]any, error) {
	ordinal := task.ResultOrdinal
	if ordinal <= 0 {
		ordinal = 1
	}
	plan.CandidateDiagnostics = taskCandidateDiagnostics(state, task)
	candidate, found := b.resolveTaskResult(state, task, ordinal, plan)
	for attempt, delay := range []string{"1200", "1800"} {
		if found || browserChallengeDetected(state) || browserInterventionRequired(state) {
			break
		}
		var waitErr error
		message := "Waiting for page content"
		if attempt > 0 {
			message = "Waiting for dynamic page items"
		}
		state, waitErr = b.runTaskAction(ctx, request, "wait", map[string]any{"value": delay, "snapshot": true}, message, 82+attempt*2)
		if waitErr != nil {
			return state, waitErr
		}
		plan.CandidateDiagnostics = taskCandidateDiagnostics(state, task)
		candidate, found = b.resolveTaskResult(state, task, ordinal, plan)
	}
	if !found && plan.Resolution != nil && (plan.Resolution.RequiresVisual || plan.Resolution.RequiresSpatial) {
		var captureErr error
		arguments := map[string]any{
			"screenshot_scope": "viewport", "snapshot": true, "goal": plan.Goal,
			"target": task.Target, "query": task.Query, "target_resolution": true,
		}
		if selected := plan.Resolution.Selected; selected != nil && browserRefPattern.MatchString(selected.Ref) {
			arguments["screenshot_scope"] = "element"
			arguments["ref"] = selected.Ref
		}
		step, message := "capture_focused_visual_evidence", "Capturing focused evidence for target resolution"
		if plan.Resolution.RequiresSpatial && !plan.Resolution.RequiresVisual {
			step, message = "capture_spatial_evidence", "Capturing element positions for target resolution"
		}
		plan.Steps = append(plan.Steps, step)
		state, captureErr = b.runTaskAction(ctx, request, "screenshot", arguments, message, 86)
		if captureErr != nil {
			return state, captureErr
		}
		candidate, found = b.resolveTaskResult(state, task, ordinal, plan)
	}
	if !found && task.PlayResult {
		var expandErr error
		state, candidate, found, expandErr = b.expandTaskMediaCollection(ctx, request, task, state, plan, ordinal)
		if expandErr != nil {
			return state, expandErr
		}
	}
	if !found && plan.Resolution != nil && plan.Resolution.Selected != nil {
		return browserTaskResolutionIntervention(state, plan)
	}
	if found {
		if plan.CandidateSource == "" {
			plan.CandidateSource = taskCandidateSource(state, ordinal, task, candidate)
		}
		plan.SelectedLabel = candidate.Title
		plan.SelectedURL = candidate.URL
		plan.Steps = append(plan.Steps, fmt.Sprintf("open_result_%d", ordinal))
		if task.PlayResult {
			plan.Steps = append(plan.Steps, "start_playback")
			arguments := map[string]any{
				"target_url": candidate.URL, "target_label": candidate.Title,
				"expected_page_url": browserStringValue(state["url"]), "verify_playback": true, "snapshot": true,
			}
			if browserRefPattern.MatchString(candidate.Ref) {
				arguments["ref"] = candidate.Ref
			}
			return b.runTaskAction(ctx, request, "play", arguments, "Opening and verifying playback", 95)
		}
		state, err := b.runTaskAction(ctx, request, "navigate", map[string]any{
			"url": candidate.URL, "target": candidate.Title, "snapshot": true,
		}, fmt.Sprintf("Opening result %d", ordinal), 88)
		if err != nil {
			return state, err
		}
		if !browserTargetObserved(state, candidate.URL) {
			plan.Completed = false
			plan.Message = fmt.Sprintf("Result %d did not open the selected page.", ordinal)
			return state, fmt.Errorf("%s", plan.Message)
		}
		return state, nil
	}

	ref := findNthBrowserResultRef(state, ordinal, task.Target)
	if ref == "" {
		var err error
		state, err = b.runTaskAction(ctx, request, "wait", map[string]any{"value": "1200", "snapshot": true}, "Waiting for page content", 82)
		if err != nil {
			return state, err
		}
		ref = findNthBrowserResultRef(state, ordinal, task.Target)
	}
	if ref == "" {
		plan.Completed = false
		plan.Message = fmt.Sprintf("Could not identify a safe result at position %d from the current observation.", ordinal)
		return state, fmt.Errorf("%s", plan.Message)
	}
	plan.Steps = append(plan.Steps, fmt.Sprintf("open_result_%d", ordinal))
	arguments := map[string]any{
		"ref": ref, "snapshot": true, "expected_page_url": browserStringValue(state["url"]),
	}
	if element, ok := findBrowserElementByRef(state, ref); ok {
		if targetURL := resolveBrowserElementURL(browserStringValue(state["url"]), element.URL); targetURL != "" {
			arguments["target_url"] = targetURL
			plan.SelectedURL = targetURL
		}
		arguments["target_label"] = browserElementTitle(element)
	}
	state, err := b.runTaskAction(ctx, request, "click", arguments, fmt.Sprintf("Opening result %d", ordinal), 88)
	if err != nil {
		return state, err
	}
	plan.Steps = append(plan.Steps, "wait_for_page")
	state, err = b.runTaskAction(ctx, request, "wait", map[string]any{"value": "1200", "snapshot": true}, "Waiting for page", 95)
	if err != nil {
		return state, err
	}
	if task.PlayResult {
		plan.Steps = append(plan.Steps, "start_playback")
		state, err = b.runTaskAction(ctx, request, "play", map[string]any{
			"expected_page_url": browserStringValue(state["url"]), "verify_playback": true, "snapshot": true,
		}, "Starting and verifying playback", 97)
		if err != nil {
			return state, err
		}
	}
	return state, nil
}

func (b *browserController) resolveTaskResult(state map[string]any, task inferredBrowserTask, ordinal int, plan *browserTaskPlan) (browserMediaCandidate, bool) {
	if b.targetResolver == nil {
		b.targetResolver = newBrowserTargetResolver()
	}
	resolution := b.targetResolver.Resolve(state, task, ordinal, "LOW")
	plan.Resolution = &resolution
	if resolution.Decision == browserResolutionExecute && resolution.Selected != nil {
		selected := resolution.Selected
		plan.CandidateSource = selected.Source
		return browserMediaCandidate{
			ID: selected.ID, Title: selected.Label, URL: selected.URL, Ref: selected.Ref, Kind: selected.Kind,
			Position: selected.Position, Order: selected.Order,
		}, true
	}
	return browserMediaCandidate{}, false
}

func browserTaskResolutionIntervention(state map[string]any, plan *browserTaskPlan) (map[string]any, error) {
	if state == nil {
		state = make(map[string]any)
	}
	resolution := plan.Resolution
	message := "Athena found possible page targets but needs confirmation before acting."
	if resolution != nil && resolution.Decision == browserResolutionBlock {
		message = "Athena blocked the browser action because target confidence is too low for its risk."
	}
	plan.Completed = false
	plan.Message = message
	plan.Steps = append(plan.Steps, "await_target_confirmation")
	state["user_intervention_required"] = true
	state["intervention"] = map[string]any{
		"kind": "target_confirmation", "message": message, "resume_session_id": browserStringValue(state["session_id"]),
	}
	state["continuation_required"] = map[string]any{"kind": "target_confirmation", "reason": resolution.Reason}
	state["target_resolution"] = resolution
	if suggestions := browserTargetConfirmationSuggestions(browserStringValue(state["session_id"]), state, resolution); len(suggestions) > 0 {
		state["suggested_actions"] = suggestions
	}
	return state, nil
}

func taskCandidateDiagnostics(state map[string]any, task inferredBrowserTask) map[string]any {
	diagnostics := map[string]any{"preferred_kinds": append([]string(nil), task.PreferredKinds...)}
	if model, ok := perception.SemanticPage(state); ok {
		diagnostics["page_type"] = model.Type
		diagnostics["semantic_entities"] = len(model.Entities)
	}
	values, _ := state["content_candidates"].([]map[string]any)
	if values == nil {
		if raw, ok := state["content_candidates"].([]any); ok {
			values = browserMediaCandidateMaps(raw)
		}
	}
	headings, collections := 0, 0
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(browserStringValue(value["context"]))) {
		case "heading":
			headings++
		case "collection":
			collections++
		}
	}
	diagnostics["content_candidates"] = len(values)
	diagnostics["heading_candidates"] = headings
	diagnostics["collection_candidates"] = collections
	diagnostics["media_candidates"] = len(browserMediaCandidates(state))
	return diagnostics
}

func taskCandidateSource(state map[string]any, ordinal int, task inferredBrowserTask, selected browserMediaCandidate) string {
	matches := func(candidate browserMediaCandidate, found bool) bool {
		if !found {
			return false
		}
		if selected.URL != "" || candidate.URL != "" {
			return normalizeBrowserPageURL(selected.URL) == normalizeBrowserPageURL(candidate.URL)
		}
		return selected.Ref != "" && selected.Ref == candidate.Ref
	}
	if !task.PlayResult && matches(nthStructuredContentCandidate(state, ordinal)) {
		return "structured_content"
	}
	if task.PlayResult && matches(findNthBrowserMediaCandidate(state, ordinal)) {
		return "observed_media"
	}
	return "semantic_page"
}

func taskResultCandidate(state map[string]any, ordinal int, task inferredBrowserTask) (browserMediaCandidate, bool) {
	if strings.TrimSpace(task.Query) == "" {
		candidate, found := semanticTaskCandidate(state, ordinal, task.PlayResult, task.PreferredKinds...)
		if !found && task.PlayResult {
			candidate, found = findNthBrowserMediaCandidate(state, ordinal)
		}
		return candidate, found
	}
	if candidate, found := nthRelevantStructuredTaskCandidate(state, ordinal, task.Query); found {
		return candidate, true
	}

	matched := 0
	for position := 1; position <= 40; position++ {
		candidate, found := semanticTaskCandidate(state, position, task.PlayResult, task.PreferredKinds...)
		if !found {
			break
		}
		if !taskCandidateMatchesQuery(candidate, task.Query) {
			continue
		}
		matched++
		if matched == ordinal {
			return candidate, true
		}
	}
	if task.PlayResult {
		matched = 0
		for position := 1; position <= 40; position++ {
			candidate, found := findNthBrowserMediaCandidate(state, position)
			if !found {
				break
			}
			if !taskCandidateMatchesQuery(candidate, task.Query) {
				continue
			}
			matched++
			if matched == ordinal {
				return candidate, true
			}
		}
	}
	return browserMediaCandidate{}, false
}

func nthRelevantStructuredTaskCandidate(state map[string]any, ordinal int, query string) (browserMediaCandidate, bool) {
	if ordinal <= 0 {
		return browserMediaCandidate{}, false
	}
	matched := 0
	for position := 1; position <= 40; position++ {
		candidate, found := nthStructuredContentCandidate(state, position)
		if !found {
			break
		}
		if !taskCandidateMatchesQuery(candidate, query) {
			continue
		}
		matched++
		if matched == ordinal {
			return candidate, true
		}
	}
	return browserMediaCandidate{}, false
}

func taskCandidateMatchesQuery(candidate browserMediaCandidate, query string) bool {
	query = normalizeCurrentPageSelection(query)
	label := normalizeCurrentPageSelection(candidate.Title)
	if query == "" || label == "" {
		return false
	}
	return currentPageSelectionScore(query, label) >= 55
}

// expandTaskMediaCollection handles pages that expose collections before their
// playable items. The requested ordinal still applies to the item inside the
// first visible collection, rather than to the collection itself.
func (b *browserController) expandTaskMediaCollection(
	ctx context.Context,
	request browserTaskRequest,
	task inferredBrowserTask,
	state map[string]any,
	plan *browserTaskPlan,
	ordinal int,
) (map[string]any, browserMediaCandidate, bool, error) {
	collection, found := semanticTaskCandidate(state, 1, false, "media_collection")
	if !found {
		return state, browserMediaCandidate{}, false, nil
	}

	plan.Steps = append(plan.Steps, "open_media_collection")
	state, err := b.runTaskAction(ctx, request, "navigate", map[string]any{
		"url": collection.URL, "target": collection.Title, "snapshot": true, "open_mode": "current",
	}, "Opening the first media collection", 84)
	if err != nil {
		return state, browserMediaCandidate{}, false, err
	}
	if browserChallengeDetected(state) || browserInterventionRequired(state) {
		return state, browserMediaCandidate{}, false, nil
	}

	plan.Steps = append(plan.Steps, "wait_for_collection_items")
	state, err = b.runTaskAction(ctx, request, "wait", map[string]any{
		"value": "1800", "snapshot": true,
	}, "Waiting for media collection items", 88)
	if err != nil {
		return state, browserMediaCandidate{}, false, err
	}
	candidate, found := taskResultCandidate(state, ordinal, task)
	return state, candidate, found, nil
}

func (b *browserController) runTaskActionOnce(ctx context.Context, request browserTaskRequest, action string, arguments map[string]any, message string, progressValue int) (map[string]any, error) {
	if request.Progress != nil {
		request.Progress(browserActionProgress{Stage: action, Progress: progressValue, Message: message, State: map[string]any{"action": action}})
	}
	timeout := browserTaskActionTimeout(action, arguments)
	actionCtx, cancel := context.WithTimeout(ctx, timeout)
	policy := browserTaskActionPolicy(action, arguments)
	state, err := b.runAction(actionCtx, browserExecuteRequest{
		RequestID: request.RequestID, SessionID: request.SessionID, Action: action, Arguments: arguments,
		RiskLevel: policy.Risk, Decision: policy.Decision, Approved: policy.Decision == "ALLOW", Progress: request.Progress,
	})
	actionErr := actionCtx.Err()
	cancel()
	if actionErr == nil {
		return state, err
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return state, context.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return state, fmt.Errorf("browser task deadline exceeded during %s: %w", action, ctx.Err())
	}
	timeoutErr := fmt.Errorf("browser %s action exceeded its %s execution budget: %w", action, timeout, actionErr)
	if request.Progress != nil {
		request.Progress(browserActionProgress{
			Stage: "failed", Progress: 100, Message: timeoutErr.Error(),
			State: map[string]any{"action": action, "timeout_ms": timeout.Milliseconds()},
		})
	}
	return state, timeoutErr
}

func browserTaskActionTimeout(action string, arguments map[string]any) time.Duration {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "play":
		return 45 * time.Second
	case "navigate", "extract":
		return 40 * time.Second
	case "wait":
		milliseconds, _ := strconv.Atoi(browserStringValue(arguments["value"]))
		if milliseconds > 0 {
			return time.Duration(milliseconds)*time.Millisecond + 10*time.Second
		}
		return 20 * time.Second
	case "screenshot":
		return 30 * time.Second
	default:
		return 30 * time.Second
	}
}

func (b *browserController) finishTask(state map[string]any, plan browserTaskPlan, err error) (map[string]any, error) {
	if state == nil {
		state = make(map[string]any)
	}
	if browserChallengeDetected(state) {
		plan.Completed = false
		plan.Message = "Human verification is required before this browser task can continue."
	} else if browserInterventionRequired(state) {
		plan.Completed = false
		plan.Message = "User confirmation or takeover is required before this browser task can continue."
	} else if err != nil {
		plan.Completed = false
		if plan.Message == "" || plan.Message == "Browser task completed." {
			plan.Message = err.Error()
		}
	} else if err == nil && plan.Message == "" {
		plan.Message = "Browser task completed."
	}
	plan.ExecutionBudget = plan.budget.snapshot()
	plan.Interactions = plan.trace.snapshot()
	if plan.effectTrace != nil {
		err = plan.effectTrace.finish(state, &plan, err)
	}
	state["browser_task"] = plan
	return state, err
}

func taskTargetURL(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return target
	}
	if knowledge, matched := siteknowledge.MatchTarget(target); matched {
		return knowledge.HomeURL
	}
	return ""
}

func taskSearchURL(task inferredBrowserTask) string {
	if targetURL := knownTaskSearchURL(task); targetURL != "" {
		return targetURL
	}
	return ""
}

func knownTaskSearchURL(task inferredBrowserTask) string {
	knowledge, matched := siteknowledge.MatchTarget(task.Target)
	if !matched {
		return ""
	}
	return siteknowledge.SearchURL(knowledge, strings.TrimSpace(task.Query), task.PreferredKinds...)
}

func semanticSearchInputRef(state map[string]any) string {
	for _, interaction := range perception.InteractionCandidates(state) {
		if interaction.Kind == "search" && browserRefPattern.MatchString(interaction.InputRef) {
			return interaction.InputRef
		}
	}
	return findBrowserElementRef(state, isBrowserSearchBox)
}

func semanticTaskCandidate(state map[string]any, ordinal int, playable bool, preferredKinds ...string) (browserMediaCandidate, bool) {
	model, ok := perception.SemanticPage(state)
	if !ok || ordinal <= 0 {
		return browserMediaCandidate{}, false
	}
	preferred := make(map[string]bool, len(preferredKinds))
	for _, kind := range preferredKinds {
		preferred[strings.ToLower(strings.TrimSpace(kind))] = true
	}
	// Feeds often link to arbitrary external URLs whose paths do not describe
	// the page entity. In that case the repeated heading/card structure is a
	// stronger signal than URL words such as /post/ or /article/.
	if !playable && prefersStructuredFeed(preferred) {
		if candidate, found := nthStructuredContentCandidate(state, ordinal); found {
			return candidate, true
		}
		if entity, found := nthStructuredFeedEntity(model.Entities, ordinal); found {
			return browserCandidateFromEntity(entity), true
		}
	}
	matched := 0
	var entity perception.SemanticEntity
	found := false
	for _, candidate := range model.Entities {
		kind := strings.ToLower(strings.TrimSpace(candidate.Kind))
		if len(preferred) > 0 && !preferred[kind] {
			continue
		}
		if len(preferred) == 0 && playable && kind != "video" && kind != "audio" {
			continue
		}
		hasURL := strings.TrimSpace(candidate.URL) != ""
		hasRef := browserRefPattern.MatchString(candidate.Ref)
		// Dynamic media feeds sometimes expose a stable accessibility ref before
		// their client-side link URL is available. The ref is still safe to use:
		// the play action refreshes it by label and verifies real playback after
		// activation. Ordinary navigation continues to require a concrete URL.
		if (!hasURL && !(playable && hasRef)) || isLowValueBrowserTarget(candidate.Label, candidate.URL) || isCurrentSiteNavigationCandidate(browserStringValue(state["url"]), candidate) {
			continue
		}
		matched++
		if matched == ordinal {
			entity, found = candidate, true
			break
		}
	}
	if !found && playable {
		if contentCandidate, contentFound := nthSemanticMediaContentCandidate(state, model, ordinal); contentFound {
			return contentCandidate, true
		}
		if mediaEntity, mediaFound := nthSemanticMediaFeedEntity(model, ordinal); mediaFound {
			return browserCandidateFromEntity(mediaEntity), true
		}
	}
	if !found {
		return browserMediaCandidate{}, false
	}
	return browserCandidateFromEntity(entity), true
}

func isCurrentSiteNavigationCandidate(currentURL string, candidate perception.SemanticEntity) bool {
	if strings.EqualFold(strings.TrimSpace(candidate.Kind), "navigation") {
		return true
	}
	current, currentErr := url.Parse(strings.TrimSpace(currentURL))
	target, targetErr := url.Parse(strings.TrimSpace(candidate.URL))
	if currentErr != nil || targetErr != nil || current.Hostname() == "" || target.Hostname() == "" || !strings.EqualFold(current.Hostname(), target.Hostname()) {
		return false
	}
	if normalizeBrowserPageURL(current.String()) == normalizeBrowserPageURL(target.String()) {
		return true
	}
	targetPath := strings.ToLower(strings.TrimSpace(target.Path))
	return (targetPath == "" || targetPath == "/") && strings.TrimSpace(current.Path) != "" && strings.TrimSpace(current.Path) != "/"
}

func nthStructuredContentCandidate(state map[string]any, ordinal int) (browserMediaCandidate, bool) {
	if ordinal <= 0 {
		return browserMediaCandidate{}, false
	}
	values, _ := state["content_candidates"].([]map[string]any)
	if values == nil {
		if raw, ok := state["content_candidates"].([]any); ok {
			values = browserMediaCandidateMaps(raw)
		}
	}
	type orderedCandidate struct {
		browserMediaCandidate
		Order int
	}
	groups := map[string][]orderedCandidate{"heading": {}, "collection": {}}
	seen := make(map[string]bool, len(values))
	currentURL := browserStringValue(state["url"])
	for _, value := range values {
		contextKind := strings.ToLower(strings.TrimSpace(browserStringValue(value["context"])))
		if contextKind != "heading" && contextKind != "collection" {
			continue
		}
		title := browserStringValue(value["title"])
		targetURL := resolveBrowserElementURL(currentURL, browserStringValue(value["url"]))
		normalized := normalizeBrowserPageURL(targetURL)
		if title == "" || targetURL == "" || normalized == "" || seen[normalized] ||
			isLowValueBrowserTarget(title, targetURL) || isBrowserSearchSuggestionURL(targetURL) {
			continue
		}
		seen[normalized] = true
		kind, candidateID := browserMediaIdentity(targetURL)
		if kind == "" {
			kind = "content"
		}
		if candidateID == "" {
			candidateID = stableBrowserMediaID(targetURL)
		}
		groups[contextKind] = append(groups[contextKind], orderedCandidate{
			browserMediaCandidate: browserMediaCandidate{
				ID: candidateID, Title: title, URL: targetURL, Kind: kind,
				Ref: browserMediaCandidateRef(state, targetURL), Position: int(int64Argument(value["position"])),
			},
			Order: int(int64Argument(value["order"])),
		})
	}
	for _, contextKind := range []string{"heading", "collection"} {
		candidates := groups[contextKind]
		if len(candidates) < ordinal {
			continue
		}
		sort.SliceStable(candidates, func(left, right int) bool {
			if candidates[left].Position == candidates[right].Position {
				if candidates[left].Order != candidates[right].Order {
					return candidates[left].Order < candidates[right].Order
				}
				return candidates[left].ID < candidates[right].ID
			}
			return candidates[left].Position < candidates[right].Position
		})
		return candidates[ordinal-1].browserMediaCandidate, true
	}
	return browserMediaCandidate{}, false
}

func isBrowserSearchSuggestionURL(address string) bool {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return false
	}
	for key := range parsed.Query() {
		normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
		if strings.Contains(normalized, "suggest") || strings.Contains(normalized, "autocomplete") {
			return true
		}
	}
	for _, part := range strings.Split(strings.ToLower(strings.Trim(parsed.Path, "/")), "/") {
		if strings.Contains(part, "suggest") || strings.Contains(part, "autocomplete") {
			return true
		}
	}
	return false
}

func nthSemanticMediaContentCandidate(state map[string]any, model perception.SemanticPageModel, ordinal int) (browserMediaCandidate, bool) {
	if ordinal <= 0 || !semanticPageHasMediaContext(model) {
		return browserMediaCandidate{}, false
	}
	values, _ := state["content_candidates"].([]map[string]any)
	if values == nil {
		if raw, ok := state["content_candidates"].([]any); ok {
			values = browserMediaCandidateMaps(raw)
		}
	}
	type rankedCandidate struct {
		candidate browserMediaCandidate
		rank      int
		order     int
	}
	ranked := make([]rankedCandidate, 0, len(values))
	pageHost := browserURLHost(model.URL)
	for _, value := range values {
		title := browserStringValue(value["title"])
		targetURL := resolveBrowserElementURL(model.URL, browserStringValue(value["url"]))
		if title == "" || targetURL == "" || isLowValueBrowserTarget(title, targetURL) {
			continue
		}
		contextKind := strings.ToLower(strings.TrimSpace(browserStringValue(value["context"])))
		structured, _ := value["structured"].(bool)
		if contextKind != "collection" && contextKind != "heading" && !structured {
			continue
		}
		rank := browserURLPathDepth(targetURL) * 20
		if browserURLHost(targetURL) == pageHost {
			rank += 30
		}
		switch contextKind {
		case "collection":
			rank += 80
		case "heading":
			rank += 55
		default:
			rank += 35
		}
		rank += int(int64Argument(value["score"]))
		kind, mediaID := browserMediaIdentity(targetURL)
		if mediaID == "" {
			mediaID = stableBrowserMediaID(targetURL)
		}
		ranked = append(ranked, rankedCandidate{
			candidate: browserMediaCandidate{
				ID: mediaID, Title: title, URL: targetURL, Kind: kind,
				Ref: browserMediaCandidateRef(state, targetURL), Position: int(int64Argument(value["position"])),
			},
			rank: rank, order: int(int64Argument(value["order"])),
		})
	}
	if len(ranked) == 0 {
		return browserMediaCandidate{}, false
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].rank != ranked[right].rank {
			return ranked[left].rank > ranked[right].rank
		}
		if ranked[left].candidate.Position != ranked[right].candidate.Position {
			return ranked[left].candidate.Position < ranked[right].candidate.Position
		}
		return ranked[left].order < ranked[right].order
	})
	bestRank := ranked[0].rank
	best := make([]rankedCandidate, 0, len(ranked))
	for _, candidate := range ranked {
		if candidate.rank != bestRank {
			break
		}
		best = append(best, candidate)
	}
	if ordinal > len(best) {
		return browserMediaCandidate{}, false
	}
	return best[ordinal-1].candidate, true
}

// nthSemanticMediaFeedEntity bridges generic page understanding to action
// selection when a site's media URLs do not contain words such as song or
// video. It relies on the site-independent page model: an observed media
// controller plus a repeated content collection is stronger evidence than a
// URL naming convention.
func nthSemanticMediaFeedEntity(model perception.SemanticPageModel, ordinal int) (perception.SemanticEntity, bool) {
	if ordinal <= 0 || !semanticPageHasMediaContext(model) {
		return perception.SemanticEntity{}, false
	}
	entitiesByID := make(map[string]perception.SemanticEntity, len(model.Entities))
	for _, entity := range model.Entities {
		entitiesByID[entity.ID] = entity
	}
	type rankedSection struct {
		score    int
		entities []perception.SemanticEntity
	}
	sections := make([]rankedSection, 0, len(model.Sections))
	for _, section := range model.Sections {
		kind := strings.ToLower(strings.TrimSpace(section.Kind))
		if kind != "media_list" && kind != "item_list" {
			continue
		}
		candidates := make([]perception.SemanticEntity, 0, len(section.EntityIDs))
		maxDepth, sameOrigin, playableCount := 0, 0, 0
		for _, entityID := range section.EntityIDs {
			entity, exists := entitiesByID[entityID]
			if !exists || strings.TrimSpace(entity.URL) == "" || isLowValueBrowserTarget(entity.Label, entity.URL) {
				continue
			}
			candidates = append(candidates, entity)
			maxDepth = max(maxDepth, browserURLPathDepth(entity.URL))
			if browserURLHost(entity.URL) == browserURLHost(model.URL) {
				sameOrigin++
			}
			if entity.Playable || entity.Kind == "video" || entity.Kind == "audio" {
				playableCount++
			}
		}
		if len(candidates) < ordinal || len(candidates) < 2 {
			continue
		}
		sort.SliceStable(candidates, func(left, right int) bool {
			if candidates[left].Position == candidates[right].Position {
				return candidates[left].ID < candidates[right].ID
			}
			return candidates[left].Position < candidates[right].Position
		})
		score := min(len(candidates), 12)*5 + maxDepth*18 + sameOrigin*3 + playableCount*12
		if kind == "media_list" {
			score += 200
		}
		sections = append(sections, rankedSection{score: score, entities: candidates})
	}
	if len(sections) == 0 {
		return perception.SemanticEntity{}, false
	}
	sort.SliceStable(sections, func(left, right int) bool {
		if sections[left].score == sections[right].score {
			return sections[left].entities[0].Position < sections[right].entities[0].Position
		}
		return sections[left].score > sections[right].score
	})
	return sections[0].entities[ordinal-1], true
}

func semanticPageHasMediaContext(model perception.SemanticPageModel) bool {
	if strings.EqualFold(strings.TrimSpace(model.Type), "media_catalog") {
		return true
	}
	for _, signal := range model.Signals {
		if strings.EqualFold(strings.TrimSpace(signal), "media_controls") {
			return true
		}
	}
	return false
}

func browserURLPathDepth(address string) int {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return 0
	}
	depth := 0
	for _, part := range strings.Split(strings.Trim(parsed.Path, "/"), "/") {
		if strings.TrimSpace(part) != "" {
			depth++
		}
	}
	return depth
}

func prefersStructuredFeed(preferred map[string]bool) bool {
	return preferred["discussion"] || preferred["article"] || preferred["content"]
}

func nthStructuredFeedEntity(entities []perception.SemanticEntity, ordinal int) (perception.SemanticEntity, bool) {
	primary := make([]perception.SemanticEntity, 0, len(entities))
	secondary := make([]perception.SemanticEntity, 0, len(entities))
	seenPrimary := make(map[string]bool, len(entities))
	seenSecondary := make(map[string]bool, len(entities))
	for _, entity := range entities {
		source := strings.ToLower(strings.TrimSpace(browserStringValue(entity.Attributes["source"])))
		context := strings.ToLower(strings.TrimSpace(browserStringValue(entity.Attributes["context"])))
		focused := source == "focused_dom_content_candidate" && (context == "collection" || context == "heading")
		if source == "" && entity.SectionID == "" && (context == "collection" || context == "heading") {
			focused = true
		}
		if (!focused && entity.SectionID == "") || strings.TrimSpace(entity.URL) == "" || isLowValueBrowserTarget(entity.Label, entity.URL) {
			continue
		}
		normalized := normalizeBrowserPageURL(entity.URL)
		if normalized == "" {
			continue
		}
		if focused {
			if !seenPrimary[normalized] {
				seenPrimary[normalized] = true
				primary = append(primary, entity)
			}
			continue
		}
		if !seenSecondary[normalized] {
			seenSecondary[normalized] = true
			secondary = append(secondary, entity)
		}
	}
	orderEntities := func(candidates []perception.SemanticEntity) {
		sort.SliceStable(candidates, func(left, right int) bool {
			if candidates[left].Position == candidates[right].Position {
				return candidates[left].ID < candidates[right].ID
			}
			return candidates[left].Position < candidates[right].Position
		})
	}
	orderEntities(primary)
	if ordinal <= len(primary) {
		return primary[ordinal-1], true
	}
	orderEntities(secondary)
	if ordinal <= len(secondary) {
		return secondary[ordinal-1], true
	}
	return perception.SemanticEntity{}, false
}

func browserCandidateFromEntity(entity perception.SemanticEntity) browserMediaCandidate {
	kind, mediaID := browserMediaIdentity(entity.URL)
	if kind == "" {
		kind = entity.Kind
	}
	if mediaID == "" {
		switch {
		case strings.TrimSpace(entity.URL) != "":
			mediaID = stableBrowserMediaID(entity.URL)
		case strings.TrimSpace(entity.ID) != "":
			mediaID = entity.ID
		default:
			mediaID = entity.Ref
		}
	}
	return browserMediaCandidate{ID: mediaID, Title: entity.Label, URL: entity.URL, Kind: kind, Ref: entity.Ref, Position: entity.Position}
}

func discoveredTaskTargetCandidate(state map[string]any, target string) (browserMediaCandidate, bool) {
	entities := discoveredTaskTargetEntities(state)
	if len(entities) == 0 {
		return browserMediaCandidate{}, false
	}
	targetTerms := browserDiscoveryTerms(target)
	targetCompact := strings.Join(targetTerms, "")
	targetAcronym := browserDiscoveryAcronym(targetTerms)
	bestScore := 0
	var best perception.SemanticEntity
	for _, entity := range entities {
		parsed, err := url.Parse(strings.TrimSpace(entity.URL))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			continue
		}
		host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
		if isBrowserDiscoveryHost(host) || isLowValueBrowserTarget(entity.Label, entity.URL) {
			continue
		}
		hostCompact := strings.NewReplacer(".", "", "-", "", "_", "").Replace(host)
		label := strings.ToLower(strings.Join(strings.Fields(entity.Label), " "))
		hostMatched := targetCompact != "" && strings.Contains(hostCompact, targetCompact)
		if !hostMatched && targetAcronym != "" {
			hostMatched = strings.Contains(hostCompact, targetAcronym)
		}
		matchedTerms := 0
		matchedLabelTerms := 0
		for _, term := range targetTerms {
			if strings.Contains(hostCompact, term) {
				matchedTerms++
			}
			if strings.Contains(label, term) {
				matchedLabelTerms++
			}
		}
		if !hostMatched && len(targetTerms) > 0 && matchedTerms == len(targetTerms) {
			hostMatched = true
		}
		rootPath := strings.Trim(parsed.Path, "/") == ""
		shortBrandHostMatch := rootPath && len(targetTerms) > 1 && matchedLabelTerms == len(targetTerms) &&
			browserHostStartsWithTargetTerm(host, targetTerms)
		if !hostMatched && shortBrandHostMatch {
			// Some products deliberately use a short brand domain (for example,
			// DEV Community uses dev.to). Accept it only when the search-result
			// title contains every requested term and the result is the site root.
			hostMatched = true
		}
		// A third-party article can mention the requested brand in its title.
		// Requiring hostname evidence prevents that label-only result from being
		// mistaken for the site's official home page.
		if !hostMatched {
			continue
		}
		score := 100 + matchedTerms*35
		if shortBrandHostMatch {
			score += 140
		}
		if targetCompact != "" && strings.Contains(hostCompact, targetCompact) {
			extraCharacters := len(hostCompact) - len(targetCompact)
			closeness := 120 - extraCharacters*15
			if closeness > 0 {
				score += closeness
			}
			if extraCharacters == 0 {
				score += 200
			}
		}
		score += matchedLabelTerms * 20
		if rootPath {
			// Search engines often expose several sitelinks for one domain. The
			// root page is the only safe generic entry point; content selection
			// belongs to the page-understanding stage that follows.
			score += 180
		}
		if score > bestScore {
			bestScore, best = score, entity
		}
	}
	if bestScore < 20 {
		return browserMediaCandidate{}, false
	}
	return browserMediaCandidate{
		ID: stableBrowserMediaID(best.URL), Title: best.Label, URL: best.URL,
		Kind: best.Kind, Ref: best.Ref, Position: best.Position,
	}, true
}

func browserHostStartsWithTargetTerm(host string, terms []string) bool {
	host = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
	first, _, _ := strings.Cut(host, ".")
	first = strings.TrimSpace(first)
	if first == "" {
		return false
	}
	for _, term := range terms {
		if first == strings.ToLower(strings.TrimSpace(term)) {
			return true
		}
	}
	return false
}

func discoveredTaskTargetEntities(state map[string]any) []perception.SemanticEntity {
	result := make([]perception.SemanticEntity, 0, 40)
	seen := make(map[string]bool, 40)
	appendEntity := func(entity perception.SemanticEntity) {
		normalized := normalizeBrowserPageURL(entity.URL)
		if normalized == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		result = append(result, entity)
	}
	if model, ok := perception.SemanticPage(state); ok {
		for _, entity := range model.Entities {
			appendEntity(entity)
		}
	}
	contentCandidates, _ := state["content_candidates"].([]map[string]any)
	if contentCandidates == nil {
		if values, ok := state["content_candidates"].([]any); ok {
			contentCandidates = browserMediaCandidateMaps(values)
		}
	}
	for position, item := range contentCandidates {
		appendEntity(perception.SemanticEntity{
			ID:       fmt.Sprintf("discovery-content-%d", position),
			Kind:     "content",
			Label:    browserStringValue(item["title"]),
			URL:      browserStringValue(item["url"]),
			Position: position + 1,
		})
	}
	for position, element := range browserSemanticElements(state) {
		appendEntity(perception.SemanticEntity{
			ID:       fmt.Sprintf("discovery-element-%d", position),
			Kind:     "content",
			Label:    element.Label,
			Ref:      element.Ref,
			URL:      element.URL,
			Position: position + 1,
		})
	}
	return result
}

func browserDiscoveryAcronym(terms []string) string {
	if len(terms) < 2 {
		return ""
	}
	var result strings.Builder
	for _, term := range terms {
		runes := []rune(term)
		if len(runes) > 0 {
			result.WriteRune(runes[0])
		}
	}
	if result.Len() < 2 {
		return ""
	}
	return result.String()
}

func browserDiscoveryTerms(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", " ", "_", " ", ".", " ", "/", " ", "'", " ", `"`, " ").Replace(value)
	result := make([]string, 0, 4)
	for _, term := range strings.Fields(value) {
		if len([]rune(term)) < 2 {
			continue
		}
		switch term {
		case "the", "website", "site", "official", "web", "home", "page", "网站", "官网", "官方":
			continue
		}
		result = append(result, term)
	}
	return result
}

func isBrowserDiscoveryHost(host string) bool {
	host = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(host), "www."))
	for _, searchHost := range []string{"google.com", "bing.com", "duckduckgo.com", "search.yahoo.com"} {
		if host == searchHost || strings.HasSuffix(host, "."+searchHost) {
			return true
		}
	}
	return false
}

func browserTargetObserved(state map[string]any, expectedURL string) bool {
	if browserErrorPageDetected(state) {
		return false
	}
	currentURL := browserStringValue(state["url"])
	if normalizeBrowserPageURL(currentURL) == normalizeBrowserPageURL(expectedURL) {
		return true
	}
	expectedKind, expectedID := browserMediaIdentity(expectedURL)
	currentKind, currentID := browserMediaIdentity(currentURL)
	if expectedKind != "" && expectedKind == currentKind && expectedID != "" && expectedID == currentID {
		return true
	}
	expectedID = browserContentIdentity(expectedURL)
	currentID = browserContentIdentity(currentURL)
	return expectedID != "" && expectedID == currentID
}

// browserContentIdentity recognizes stable item identifiers across common
// redirect shapes, such as a result-card query URL becoming /title/{id}.
func browserContentIdentity(address string) string {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	if id := firstQueryValue(parsed,
		"content_id", "contentId", "item_id", "itemId", "media_id", "mediaId", "jbv",
	); id != "" {
		return id
	}

	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+1 < len(parts); index++ {
		switch strings.ToLower(strings.TrimSpace(parts[index])) {
		case "title", "titles", "content", "contents", "item", "items", "detail", "details":
			if id := strings.TrimSpace(parts[index+1]); id != "" {
				return id
			}
		}
	}
	return ""
}

func browserErrorPageDetected(state map[string]any) bool {
	if state == nil {
		return false
	}
	if page, ok := state["page"].(map[string]any); ok && strings.EqualFold(browserStringValue(page["type"]), "error_page") {
		return true
	}
	if model, ok := perception.SemanticPage(state); ok && strings.EqualFold(strings.TrimSpace(model.Type), "error_page") {
		return true
	}
	title := strings.ToLower(strings.Join(strings.Fields(browserStringValue(state["title"])), " "))
	for _, marker := range []string{"page not found", "404 not found", "404 - not found", "error 404", "页面不存在", "找不到页面", "页面未找到", "ページが見つかりません"} {
		if title == marker || strings.HasPrefix(title, marker+" ") || strings.HasPrefix(title, marker+" -") || strings.HasPrefix(title, marker+" |") {
			return true
		}
	}
	return title == "404" || strings.HasPrefix(title, "404 -") || strings.HasPrefix(title, "404 |")
}

func isYouTubeTarget(target string, state map[string]any) bool {
	if strings.TrimSpace(target) != "" {
		return isYouTubeName(target)
	}
	for _, key := range []string{"url", "title"} {
		value, _ := state[key].(string)
		if strings.Contains(strings.ToLower(value), "youtube") {
			return true
		}
	}
	return false
}

func isQQMusicName(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(lower, "qq music") || strings.Contains(lower, "qqmusic") ||
		strings.Contains(lower, "qq 音乐") || strings.Contains(lower, "qq音乐") ||
		strings.Contains(lower, "y.qq.com")
}

func isYouTubeName(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(lower, "youtube") || strings.Contains(lower, "you tube") || strings.Contains(lower, "youtub")
}

func currentPageSelectionCandidate(state map[string]any, selection string) (browserSemanticElement, bool) {
	desired := normalizeCurrentPageSelection(selection)
	if desired == "" {
		return browserSemanticElement{}, false
	}
	bestScore := 0
	bestLabel := ""
	var best browserSemanticElement
	ambiguous := false
	for _, element := range browserSemanticElements(state) {
		if !isSafeCurrentPageSelectionElement(element) {
			continue
		}
		label := normalizeCurrentPageSelection(browserElementTitle(element))
		score := currentPageSelectionScore(desired, label)
		if score <= 0 {
			continue
		}
		switch {
		case score > bestScore:
			best, bestScore, bestLabel, ambiguous = element, score, label, false
		case score == bestScore && label != bestLabel:
			ambiguous = true
		}
	}
	return best, bestScore >= 70 && !ambiguous
}

func isSafeCurrentPageSelectionElement(element browserSemanticElement) bool {
	label := strings.ToLower(strings.TrimSpace(element.Label))
	if element.Ref == "" || label == "" || containsBrowserNavigationNoise(label) {
		return false
	}
	for _, role := range []string{"link", "button", "option", "menuitem", "tab", "radio"} {
		if element.Role == role || strings.Contains(label, role) {
			return true
		}
	}
	return false
}

func currentPageSelectionScore(desired, label string) int {
	if desired == "" || label == "" || len([]rune(label)) < 2 {
		return 0
	}
	switch {
	case desired == label:
		return 100
	case strings.HasPrefix(desired, label+" "):
		return 98
	case strings.HasSuffix(desired, " "+label):
		return 96
	case strings.Contains(label, desired):
		return 92
	case strings.Contains(desired, label):
		return 80
	}
	wantedTokens := currentPageSelectionTokens(desired)
	labelTokens := currentPageSelectionTokens(label)
	if len(wantedTokens) == 0 || len(labelTokens) == 0 {
		return 0
	}
	matches := 0
	for token := range labelTokens {
		if wantedTokens[token] {
			matches++
		}
	}
	if matches == 0 {
		return 0
	}
	return 50 + (40*matches)/len(labelTokens)
}

func normalizeCurrentPageSelection(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(
		"-", " ", "_", " ", "/", " ", "|", " ", ":", " ",
		"《", " ", "》", " ", "「", " ", "」", " ", "『", " ", "』", " ",
	).Replace(value)
	value = strings.Trim(value, ` "'()[]{}<>:：,，。.;；!?！？`)
	return strings.Join(strings.Fields(value), " ")
}

func currentPageSelectionTokens(value string) map[string]bool {
	tokens := make(map[string]bool)
	for _, token := range strings.Fields(value) {
		switch token {
		case "the", "this", "that", "a", "an", "profile", "account", "option", "item", "button", "link", "page":
			continue
		}
		tokens[token] = true
	}
	return tokens
}

func browserSemanticElements(state map[string]any) []browserSemanticElement {
	raw, ok := state["key_elements"].([]map[string]string)
	if ok {
		return mapBrowserSemanticElements(raw)
	}
	items, ok := state["key_elements"].([]any)
	if !ok {
		return nil
	}
	elements := make([]browserSemanticElement, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ref, _ := entry["ref"].(string)
		label, _ := entry["label"].(string)
		if browserRefPattern.MatchString(ref) && strings.TrimSpace(label) != "" {
			label = strings.TrimSpace(label)
			urlValue := browserStringValue(entry["url"])
			if urlValue == "" {
				urlValue = browserElementURL(label)
			}
			elements = append(elements, browserSemanticElement{
				Ref: ref, Label: label, URL: urlValue,
				Role: strings.ToLower(browserStringValue(entry["role"])),
				Kind: strings.ToLower(browserStringValue(entry["kind"])),
			})
		}
	}
	return elements
}

func mapBrowserSemanticElements(items []map[string]string) []browserSemanticElement {
	elements := make([]browserSemanticElement, 0, len(items))
	for _, item := range items {
		ref := strings.TrimSpace(item["ref"])
		label := strings.TrimSpace(item["label"])
		if browserRefPattern.MatchString(ref) && label != "" {
			urlValue := strings.TrimSpace(item["url"])
			if urlValue == "" {
				urlValue = browserElementURL(label)
			}
			elements = append(elements, browserSemanticElement{
				Ref: ref, Label: label, URL: urlValue,
				Role: strings.ToLower(strings.TrimSpace(item["role"])),
				Kind: strings.ToLower(strings.TrimSpace(item["kind"])),
			})
		}
	}
	return elements
}

func findBrowserElementRef(state map[string]any, predicate func(browserSemanticElement) bool) string {
	for _, element := range browserSemanticElements(state) {
		if predicate(element) {
			return element.Ref
		}
	}
	return ""
}

func findBrowserElementByRef(state map[string]any, ref string) (browserSemanticElement, bool) {
	for _, element := range browserSemanticElements(state) {
		if element.Ref == ref {
			return element, true
		}
	}
	return browserSemanticElement{}, false
}

func findNthBrowserElementRef(state map[string]any, ordinal int, predicate func(browserSemanticElement) bool) string {
	if ordinal <= 0 {
		return ""
	}
	matched := 0
	for _, element := range browserSemanticElements(state) {
		if !predicate(element) {
			continue
		}
		matched++
		if matched == ordinal {
			return element.Ref
		}
	}
	return ""
}

func findNthBrowserResultRef(state map[string]any, ordinal int, target string) string {
	if !isYouTubeName(target) {
		return findNthBrowserElementRef(state, ordinal, func(element browserSemanticElement) bool {
			return isLikelyResultElement(element, target)
		})
	}
	if ordinal <= 0 {
		return ""
	}
	matched := 0
	seenVideos := make(map[string]bool)
	for _, element := range browserSemanticElements(state) {
		videoID := youtubeVideoID(element.URL)
		if videoID == "" || seenVideos[videoID] || !isLikelyResultElement(element, target) {
			continue
		}
		seenVideos[videoID] = true
		matched++
		if matched == ordinal {
			return element.Ref
		}
	}
	return ""
}

func isBrowserSearchBox(element browserSemanticElement) bool {
	label := strings.ToLower(element.Label)
	return strings.Contains(label, "search") || strings.Contains(label, "搜索") || strings.Contains(label, "搜尋")
}

func isLikelyResultElement(element browserSemanticElement, target string) bool {
	label := strings.ToLower(element.Label)
	if strings.Contains(label, "search") || strings.Contains(label, "home") || strings.Contains(label, "shorts") || strings.Contains(label, "subscriptions") {
		return false
	}
	if isYouTubeName(target) {
		return youtubeVideoID(element.URL) != ""
	}
	return strings.Contains(label, "link") || strings.Contains(label, "http")
}

func browserElementURL(label string) string {
	lower := strings.ToLower(label)
	index := strings.Index(lower, "url=")
	if index < 0 {
		return ""
	}
	value := label[index+len("url="):]
	if end := strings.IndexAny(value, "] ,\t\r\n"); end >= 0 {
		value = value[:end]
	}
	return strings.TrimSpace(value)
}

func youtubeVideoID(address string) string {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || !strings.Contains(strings.ToLower(parsed.Host), "youtube.com") || parsed.Path != "/watch" {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("v"))
}

func isYouTubeWatchPage(state map[string]any) bool {
	address, _ := state["url"].(string)
	return youtubeVideoID(address) != ""
}

func isYouTubePlayButton(element browserSemanticElement) bool {
	label := strings.ToLower(strings.TrimSpace(element.Label))
	return strings.HasPrefix(label, `button "play`) || strings.Contains(label, `button "play (`)
}
