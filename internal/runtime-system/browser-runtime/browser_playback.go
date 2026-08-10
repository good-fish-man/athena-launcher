package browser_runtime

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"athena-launcher/internal/runtime-system/browser-runtime/siteknowledge"
)

const browserPlaybackScript = `(async()=>{const items=[...document.querySelectorAll('video,audio')];const media=items.find(x=>{const r=x.getBoundingClientRect();return r.width>0&&r.height>0})||items.find(x=>x.currentSrc||x.src)||items[0];if(!media){const state=navigator.mediaSession&&navigator.mediaSession.playbackState?String(navigator.mediaSession.playbackState):"none";return {found:state!=="none",playing:state==="playing",paused:state==="paused",ended:false,kind:"media_session",ready_state:0,current_time:0,duration:0,progressed:false,error:state==="none"?"No HTML media element or active MediaSession was found":"",title:document.title||""}}const before=Number(media.currentTime||0);let error="";try{const pending=media.play();if(pending&&typeof pending.then==="function")await Promise.race([pending,new Promise((_,reject)=>setTimeout(()=>reject(new Error("media.play() did not settle within 3000ms")),3000))])}catch(e){error=String(e&&e.message?e.message:e)}await new Promise(r=>setTimeout(r,700));const after=Number(media.currentTime||0);return {found:true,playing:!media.paused&&!media.ended,paused:Boolean(media.paused),ended:Boolean(media.ended),kind:String(media.tagName||"").toLowerCase(),ready_state:Number(media.readyState||0),current_time:after,duration:Number.isFinite(media.duration)?Number(media.duration):0,progressed:after>before,error:error,title:document.title||""}})()`

const browserPauseScript = `(async()=>{const items=[...document.querySelectorAll('video,audio')];const visible=items.find(x=>{const r=x.getBoundingClientRect();return r.width>0&&r.height>0});const active=items.find(x=>!x.paused&&!x.ended);const media=active||visible||items.find(x=>x.currentSrc||x.src)||items[0];if(!media){const state=navigator.mediaSession&&navigator.mediaSession.playbackState?String(navigator.mediaSession.playbackState):"none";return {found:state!=="none",playing:state==="playing",paused:state==="paused",ended:false,kind:"media_session",ready_state:0,current_time:0,duration:0,progressed:false,error:state==="none"?"No HTML media element or active MediaSession was found":"The active media session is not exposed as an HTML media element",title:document.title||""}}let error="";try{for(const item of items){if(!item.paused&&!item.ended)item.pause()}}catch(e){error=String(e&&e.message?e.message:e)}await new Promise(r=>setTimeout(r,250));return {found:true,playing:!media.paused&&!media.ended,paused:Boolean(media.paused),ended:Boolean(media.ended),kind:String(media.tagName||"").toLowerCase(),ready_state:Number(media.readyState||0),current_time:Number(media.currentTime||0),duration:Number.isFinite(media.duration)?Number(media.duration):0,progressed:false,error,title:document.title||""}})()`

const safeOverlayDismissScript = `(() => {
  const normalize = value => String(value || "").replace(/\s+/g, " ").trim().toLowerCase();
  const safe = new Map([
    ["reject all", 0], ["reject optional", 0], ["decline all", 0], ["only necessary", 0],
    ["necessary only", 0], ["continue without accepting", 0], ["拒绝全部", 0], ["全部拒绝", 0],
    ["仅必要", 0], ["只允许必要", 0], ["不同意", 0], ["すべて拒否", 0], ["必要なもののみ", 0],
    ["no thanks", 10], ["not now", 10], ["maybe later", 10], ["dismiss", 10], ["close", 10],
    ["关闭", 10], ["暂不", 10], ["以后再说", 10], ["いいえ", 10], ["今はしない", 10], ["閉じる", 10]
  ]);
  const visible = element => {
    if (!element || element.disabled) return false;
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 2 && rect.height > 2 && style.display !== "none" && style.visibility !== "hidden";
  };
  const candidates = [];
  for (const element of document.querySelectorAll('button,[role="button"],input[type="button"],input[type="submit"]')) {
    if (!visible(element)) continue;
    const label = normalize(element.getAttribute("aria-label") || element.getAttribute("title") || element.value || element.innerText || element.textContent);
    if (safe.has(label)) candidates.push({ element, label, priority: safe.get(label) });
  }
  candidates.sort((left, right) => left.priority - right.priority);
  const selected = candidates[0];
  if (!selected) return { clicked: false, reason: "safe_dismiss_control_not_found" };
  selected.element.click();
  return { clicked: true, label: selected.label, priority: selected.priority };
})()`

const activeModalPlaybackControlScript = `(() => {
  const clean = value => String(value || "").replace(/\s+/g, " ").trim();
  const visible = element => {
    if (!element || element.disabled) return false;
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 2 && rect.height > 2 && style.display !== "none" && style.visibility !== "hidden";
  };
  const roots = [...new Set([
    ...document.querySelectorAll('dialog[open],[role="dialog"],[aria-modal="true"]'),
    ...document.querySelectorAll('[class*="modal"],[class*="Modal"],[class*="dialog"],[class*="Dialog"],[class*="overlay"],[class*="Overlay"]')
  ])].filter(visible);
  const scored = [];
  for (const root of roots) {
    const style = getComputedStyle(root);
    const rect = root.getBoundingClientRect();
    let rootScore = 0;
    if (root.matches('dialog[open],[aria-modal="true"]')) rootScore += 160;
    if (root.getAttribute("role") === "dialog") rootScore += 120;
    if (style.position === "fixed" || style.position === "absolute") rootScore += 30;
    if (rect.width * rect.height >= innerWidth * innerHeight * 0.08) rootScore += 20;
    for (const control of root.querySelectorAll('button,[role="button"],a[href]')) {
      if (!visible(control)) continue;
      const label = clean(control.getAttribute("aria-label") || control.getAttribute("title") || control.innerText || control.textContent);
      const metadata = clean([
        control.id,
        typeof control.className === "string" ? control.className : "",
        control.getAttribute("data-testid"),
        control.getAttribute("role")
      ].filter(Boolean).join(" "));
      if (/(?:close|dismiss|cancel|trailer|preview|关闭|取消|预告)/i.test(label + " " + metadata)) continue;
      let controlScore = 0;
      if (/^(?:play|resume|watch|listen|start playback|continue watching|播放|继续播放|观看|收听)$/i.test(label)) controlScore += 220;
      else if (/(?:^|\b)(?:play|resume|watch|listen)(?:\b|$)|播放|继续播放|观看|收听/i.test(label)) controlScore += 130;
      if (/(?:^|[-_\s])play(?:$|[-_\s])|播放/i.test(metadata)) controlScore += 70;
      if (control.matches('button,[role="button"]')) controlScore += 20;
      if (control.tagName === "A" && control.hasAttribute("href")) controlScore += 15;
      if (controlScore >= 120) scored.push({ element: control, score: rootScore + controlScore, label });
    }
  }
  scored.sort((left, right) => right.score - left.score);
  document.querySelectorAll('[data-athena-modal-play-control]').forEach(element => element.removeAttribute('data-athena-modal-play-control'));
  const selected = scored[0];
  if (!selected) return { found: false, clicked: false, reason: "active_modal_play_control_not_found" };
  selected.element.setAttribute('data-athena-modal-play-control', 'true');
  return {
    found: true,
    clicked: false,
    reason: "active_modal_play_control",
    label: selected.label,
    score: selected.score,
    selector: '[data-athena-modal-play-control="true"]'
  };
})()`

const protectedPlaybackDiagnosisScript = `(async () => {
  const text = String(document.body && document.body.innerText || "").replace(/\s+/g, " ").trim();
  const pageError = /pardon the interruption|trouble with your request|playback error|unable to play|error code|播放错误|无法播放/i.test(text);
  if (!pageError) return { found: false, clicked: false, page_error: false };
  let drmSupported = false;
  let drmReason = "encrypted media API unavailable";
  if (navigator.requestMediaKeySystemAccess) {
    try {
      await navigator.requestMediaKeySystemAccess("com.widevine.alpha", [{
        initDataTypes: ["cenc"],
        audioCapabilities: [{ contentType: "audio/mp4; codecs=mp4a.40.2" }],
        videoCapabilities: [{ contentType: "video/mp4; codecs=avc1.42E01E" }]
      }]);
      drmSupported = true;
      drmReason = "";
    } catch (error) {
      drmReason = String(error && error.message || error);
    }
  }
  return {
    found: true,
    clicked: false,
    page_error: true,
    page_message: text.slice(0, 320),
    drm_checked: true,
    drm_supported: drmSupported,
    drm_reason: drmReason
  };
})()`

// activateBrowserMediaCandidate prefers a play control inside the selected
// semantic card. This keeps SPA state intact and avoids treating a title link
// as the playback action on media-heavy pages.
func activateBrowserMediaCandidate(run browserCommandRunner, sessionArgs []string, targetURL, targetLabel string) (map[string]any, error) {
	if run == nil || strings.TrimSpace(targetURL) == "" {
		return nil, nil
	}
	payload, err := json.Marshal(map[string]string{
		"target_url":   strings.TrimSpace(targetURL),
		"target_label": strings.TrimSpace(targetLabel),
	})
	if err != nil {
		return nil, fmt.Errorf("encode media candidate activation: %w", err)
	}
	script := fmt.Sprintf(`(()=>{const input=%s;const visible=element=>{if(!element||element.disabled)return false;const rect=element.getBoundingClientRect();const style=getComputedStyle(element);return rect.width>2&&rect.height>2&&style.display!=="none"&&style.visibility!=="hidden"};const normalize=value=>{try{const parsed=new URL(value,location.href);parsed.hash="";return parsed.href.replace(/\/$/,"")}catch(_){return ""}};const wanted=normalize(input.target_url);const expected=String(input.target_label||"").replace(/\s+/g," ").trim().toLowerCase();const anchors=[...document.querySelectorAll("a[href]")].filter(anchor=>visible(anchor)&&normalize(anchor.href)===wanted);anchors.sort((left,right)=>{const l=String(left.getAttribute("aria-label")||left.getAttribute("title")||left.innerText||left.textContent||"").replace(/\s+/g," ").trim().toLowerCase();const r=String(right.getAttribute("aria-label")||right.getAttribute("title")||right.innerText||right.textContent||"").replace(/\s+/g," ").trim().toLowerCase();return Number(Boolean(expected&&r.includes(expected)))-Number(Boolean(expected&&l.includes(expected)))});const anchor=anchors[0];if(!anchor)return {found:false,clicked:false,reason:"candidate_not_visible"};const score=element=>{if(!visible(element)||element===anchor)return -1000;const own=[element.getAttribute("aria-label"),element.getAttribute("title"),element.getAttribute("role"),element.getAttribute("data-testid"),element.id,typeof element.className==="string"?element.className:"",element.innerText].filter(Boolean).join(" ");const descendants=[...element.querySelectorAll("[aria-label],[title],[class],[data-testid]")].slice(0,24).map(node=>[node.getAttribute("aria-label"),node.getAttribute("title"),node.getAttribute("data-testid"),node.id,typeof node.className==="string"?node.className:""].filter(Boolean).join(" ")).join(" ");const label=(own+" "+descendants).replace(/\s+/g," ").trim();if(/(?:log\s*in|sign\s*in|login|登录|注册|download|下载|share|分享|add\s+to|添加|next|下一|previous|上一|menu|菜单|more|更多)/i.test(label))return -500;let value=0;if(/(?:^|\b)(?:play|listen|watch|resume)(?:\b|$)|播放|试听|观看|继续播放/i.test(own))value+=110;if(/(?:play|播放)/i.test([element.id,typeof element.className==="string"?element.className:"",element.getAttribute("data-testid")].filter(Boolean).join(" ")))value+=85;if(/(?:play|播放)/i.test(descendants))value+=75;if(element.matches("button,[role=button]"))value+=20;if(element.tagName==="A"&&!element.hasAttribute("href"))value+=15;return value};let ancestor=anchor.parentElement;let best=null;for(let depth=0;ancestor&&depth<7;depth++,ancestor=ancestor.parentElement){if(ancestor.matches("body,main,[role=main]"))break;const controls=[...ancestor.querySelectorAll("button,[role=button],a")];if(controls.length>24)continue;for(const control of controls){const candidateScore=score(control)-depth*4;if(candidateScore>=60&&(!best||candidateScore>best.score))best={element:control,score:candidateScore,depth}}if(best&&best.score>=100)break}if(!best)return {found:true,clicked:false,reason:"play_control_not_found",candidate_label:String(anchor.innerText||anchor.textContent||"").replace(/\s+/g," ").trim()};best.element.setAttribute("data-athena-media-activation","true");best.element.click();return {found:true,clicked:true,reason:"card_play_control",score:best.score,depth:best.depth,tag:String(best.element.tagName||"").toLowerCase(),label:String(best.element.getAttribute("aria-label")||best.element.getAttribute("title")||best.element.innerText||"").replace(/\s+/g," ").trim(),current_url:location.href}})()`, string(payload))
	output, err := run(20*time.Second, append(sessionArgs, "eval", script, "--json")...)
	if err != nil {
		return nil, fmt.Errorf("activate media candidate: %w", err)
	}
	result, ok := parseBrowserMediaActivation(output)
	if !ok {
		return nil, fmt.Errorf("browser returned an unreadable media activation result")
	}
	return result, nil
}

func parseBrowserMediaActivation(output string) (map[string]any, bool) {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &decoded) != nil {
		return nil, false
	}
	return findBrowserMediaActivation(decoded)
}

func findBrowserMediaActivation(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if _, found := typed["clicked"]; found {
			return typed, true
		}
		for _, key := range []string{"data", "result", "value"} {
			if nested, exists := typed[key]; exists {
				if result, found := findBrowserMediaActivation(nested); found {
					return result, true
				}
			}
		}
	case []any:
		for _, item := range typed {
			if result, found := findBrowserMediaActivation(item); found {
				return result, true
			}
		}
	}
	return nil, false
}

func validateBrowserPagePrecondition(run browserCommandRunner, sessionArgs []string, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" || run == nil {
		return nil
	}
	current, err := run(10*time.Second, append(sessionArgs, "get", "url")...)
	if err != nil {
		return fmt.Errorf("could not verify the current browser page: %w", err)
	}
	if normalizeBrowserPageURL(current) != normalizeBrowserPageURL(expected) {
		return fmt.Errorf("browser page changed from %q to %q; refresh the page options before retrying", expected, strings.TrimSpace(current))
	}
	return nil
}

func validateSuggestedBrowserTarget(currentURL, targetURL string) (string, error) {
	resolved := resolveBrowserElementURL(currentURL, targetURL)
	if resolved == "" {
		return "", fmt.Errorf("suggested browser target is invalid or belongs to another website")
	}
	return resolved, nil
}

func startBrowserPlayback(run browserCommandRunner, sessionArgs []string) (map[string]any, error) {
	currentURL, _ := run(10*time.Second, append(sessionArgs, "get", "url")...)
	knowledge, hasKnowledge := siteknowledge.MatchURL(currentURL)

	var state map[string]any
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		state, err = evaluateBrowserPlayback(run, sessionArgs)
		if err == nil && browserPlaybackVerified(state) {
			return state, nil
		}
		if attempt < 1 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if hasKnowledge && knowledge.Playback != nil {
		if knowledgeState, knowledgeErr := startKnowledgePlayback(run, sessionArgs, knowledge); knowledgeErr == nil {
			return knowledgeState, nil
		} else if state == nil {
			state, err = knowledgeState, knowledgeErr
		}
	}

	var activationErr error
	if selector, found, inspectErr := inspectActiveModalPlaybackControl(run, sessionArgs); inspectErr != nil {
		activationErr = inspectErr
	} else if found {
		if _, clickErr := run(15*time.Second, append(sessionArgs, "click", selector)...); clickErr != nil {
			activationErr = clickErr
		} else {
			_, _ = run(10*time.Second, append(sessionArgs, "wait", "1200")...)
			for attempt := 0; attempt < 3; attempt++ {
				state, err = evaluateBrowserPlayback(run, sessionArgs)
				if err == nil && browserPlaybackVerified(state) {
					return state, nil
				}
				if attempt < 2 {
					time.Sleep(500 * time.Millisecond)
				}
			}
			if diagnosis, diagnosisErr := inspectProtectedPlaybackFailure(run, sessionArgs); diagnosisErr != nil {
				if state == nil {
					state = make(map[string]any)
				}
				state["environment"] = diagnosis
				return state, diagnosisErr
			}
		}
	}
	for interaction := 0; interaction < 3; interaction++ {
		_, controls, observationErr := waitForBrowserPlayControls(run, sessionArgs, 15*time.Second)
		if observationErr != nil {
			activationErr = observationErr
			break
		}
		if len(controls) == 0 {
			break
		}

		clicked := false
		for _, control := range controls {
			resolvedRef, resolveErr := resolveCurrentBrowserRef(run, sessionArgs, control.Ref, browserElementTitle(control))
			if resolveErr != nil {
				activationErr = resolveErr
				continue
			}
			_, clickErr := run(15*time.Second, append(sessionArgs, "click", resolvedRef)...)
			if clickErr != nil && isCoveredBrowserElementError(clickErr) {
				if dismissed, dismissErr := dismissSafeBrowserOverlay(run, sessionArgs); dismissErr == nil && dismissed {
					resolvedRef, resolveErr = resolveCurrentBrowserRef(run, sessionArgs, control.Ref, browserElementTitle(control))
					if resolveErr == nil {
						_, clickErr = run(15*time.Second, append(sessionArgs, "click", resolvedRef)...)
					} else {
						clickErr = resolveErr
					}
				}
			}
			if clickErr != nil {
				activationErr = clickErr
				continue
			}
			clicked = true
			activationErr = nil
			break
		}
		if !clicked {
			break
		}

		// Music sites may open a dedicated player tab and initialize audio
		// asynchronously. Waiting through the browser command keeps subsequent
		// observations on the newly active tab.
		_, _ = run(10*time.Second, append(sessionArgs, "wait", "1200")...)
		for attempt := 0; attempt < 3; attempt++ {
			state, err = evaluateBrowserPlayback(run, sessionArgs)
			if err == nil && browserPlaybackVerified(state) {
				return state, nil
			}
			if attempt < 2 {
				time.Sleep(500 * time.Millisecond)
			}
		}
	}

	if err != nil {
		return state, err
	}
	if activationErr != nil {
		return state, fmt.Errorf("could not activate the visible playback control: %w", activationErr)
	}
	if diagnosis, diagnosisErr := inspectProtectedPlaybackFailure(run, sessionArgs); diagnosisErr != nil {
		if state == nil {
			state = make(map[string]any)
		}
		state["environment"] = diagnosis
		return state, diagnosisErr
	}
	if message := browserStringValue(state["error"]); message != "" {
		return state, fmt.Errorf("media did not start playing: %s", message)
	}
	return state, fmt.Errorf("media did not enter a verified playing state")
}

func pauseBrowserPlayback(run browserCommandRunner, sessionArgs []string) (map[string]any, error) {
	if run == nil {
		return nil, fmt.Errorf("browser playback runner is unavailable")
	}
	output, err := run(15*time.Second, append(sessionArgs, "eval", browserPauseScript, "--json")...)
	if err != nil {
		return nil, fmt.Errorf("could not pause browser media: %w", err)
	}
	state, ok := parseBrowserPlaybackState(output)
	if !ok {
		return nil, fmt.Errorf("browser returned an unreadable pause result")
	}
	found, _ := state["found"].(bool)
	paused, _ := state["paused"].(bool)
	playing, _ := state["playing"].(bool)
	state["verified"] = found && paused && !playing
	if verified, _ := state["verified"].(bool); !verified {
		message := strings.TrimSpace(browserStringValue(state["error"]))
		if message == "" {
			message = "the current page did not expose pausable media"
		}
		return state, fmt.Errorf("media pause was not verified: %s", message)
	}
	return state, nil
}

func inspectProtectedPlaybackFailure(run browserCommandRunner, sessionArgs []string) (map[string]any, error) {
	if run == nil {
		return nil, nil
	}
	output, err := run(12*time.Second, append(sessionArgs, "eval", protectedPlaybackDiagnosisScript, "--json")...)
	if err != nil {
		return nil, nil
	}
	result, ok := parseBrowserMediaActivation(output)
	if !ok || !boolMapState(result, "page_error") {
		return nil, nil
	}
	if checked, _ := result["drm_checked"].(bool); checked && !boolMapState(result, "drm_supported") {
		return result, fmt.Errorf("the site returned a playback error and this browser session does not support Widevine protected media; use auto-connect with a Chrome session that can play protected content")
	}
	message := browserStringValue(result["page_message"])
	if message == "" {
		message = "the website returned an error instead of starting media"
	}
	return result, fmt.Errorf("the website returned a playback error: %s", message)
}

func inspectActiveModalPlaybackControl(run browserCommandRunner, sessionArgs []string) (string, bool, error) {
	if run == nil {
		return "", false, nil
	}
	output, err := run(12*time.Second, append(sessionArgs, "eval", activeModalPlaybackControlScript, "--json")...)
	if err != nil {
		return "", false, fmt.Errorf("could not inspect active modal playback controls: %w", err)
	}
	result, ok := parseBrowserMediaActivation(output)
	if !ok {
		return "", false, fmt.Errorf("browser returned an unreadable modal playback control result")
	}
	found, _ := result["found"].(bool)
	selector := browserStringValue(result["selector"])
	return selector, found && selector != "", nil
}

func isCoveredBrowserElementError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, " is covered by ") || strings.Contains(message, "covering element")
}

// dismissSafeBrowserOverlay only runs after the browser proves that an
// overlay blocked a requested action. It prefers privacy-preserving choices
// and never opts into optional cookies or subscriptions.
func dismissSafeBrowserOverlay(run browserCommandRunner, sessionArgs []string) (bool, error) {
	if run == nil {
		return false, nil
	}
	if output, evalErr := run(12*time.Second, append(sessionArgs, "eval", safeOverlayDismissScript, "--json")...); evalErr == nil {
		if result, ok := parseBrowserMediaActivation(output); ok && boolMapState(result, "clicked") {
			_, _ = run(5*time.Second, append(sessionArgs, "wait", "400")...)
			return true, nil
		}
	}
	snapshot, err := run(15*time.Second, append(sessionArgs, "snapshot", "-i", "-c", "-d", "6")...)
	if err != nil {
		return false, err
	}
	elements := browserSemanticElements(map[string]any{"key_elements": browserKeyElements(snapshot, 120)})
	sort.SliceStable(elements, func(left, right int) bool {
		return safeOverlayDismissPriority(browserElementTitle(elements[left])) < safeOverlayDismissPriority(browserElementTitle(elements[right]))
	})
	for _, element := range elements {
		if !browserRefPattern.MatchString(element.Ref) || safeOverlayDismissPriority(browserElementTitle(element)) >= 100 {
			continue
		}
		if _, clickErr := run(12*time.Second, append(sessionArgs, "click", element.Ref)...); clickErr != nil {
			continue
		}
		_, _ = run(5*time.Second, append(sessionArgs, "wait", "400")...)
		return true, nil
	}
	return false, nil
}

func safeOverlayDismissPriority(label string) int {
	label = strings.ToLower(strings.Join(strings.Fields(label), " "))
	switch label {
	case "reject all", "reject optional", "decline all", "only necessary", "necessary only", "continue without accepting",
		"拒绝全部", "全部拒绝", "仅必要", "只允许必要", "不同意", "すべて拒否", "必要なもののみ":
		return 0
	case "no thanks", "not now", "maybe later", "dismiss", "close", "关闭", "暂不", "以后再说", "いいえ", "今はしない", "閉じる":
		return 10
	default:
		return 100
	}
}

func startQQMusicPlayback(run browserCommandRunner, sessionArgs []string) (map[string]any, error) {
	knowledge, matched := siteknowledge.MatchTarget("QQ Music")
	if !matched || knowledge.Playback == nil {
		return nil, fmt.Errorf("playback knowledge is unavailable")
	}
	return startKnowledgePlayback(run, sessionArgs, knowledge)
}

func inspectQQMusicPlaybackControl(run browserCommandRunner, sessionArgs []string) (string, string, bool, error) {
	knowledge, matched := siteknowledge.MatchTarget("QQ Music")
	if !matched || knowledge.Playback == nil {
		return "", "", false, fmt.Errorf("playback knowledge is unavailable")
	}
	return inspectKnowledgePlaybackControl(run, sessionArgs, knowledge.Playback.Controls)
}

func startKnowledgePlayback(run browserCommandRunner, sessionArgs []string, knowledge siteknowledge.Definition) (map[string]any, error) {
	if knowledge.Playback == nil {
		return nil, fmt.Errorf("playback knowledge is unavailable for %s", knowledge.DisplayName)
	}
	var state map[string]any
	var lastErr error
	for transition := 0; transition < 6; transition++ {
		state, lastErr = evaluateBrowserPlayback(run, sessionArgs)
		if lastErr == nil && browserPlaybackVerified(state) {
			return state, nil
		}
		control, selector, playing, inspectErr := inspectKnowledgePlaybackControl(run, sessionArgs, knowledge.Playback.Controls)
		if inspectErr != nil {
			if isBrowserEvaluationNavigationError(inspectErr) {
				_, _ = run(10*time.Second, append(sessionArgs, "wait", "900")...)
				continue
			}
			return state, inspectErr
		}
		if playing {
			_, _ = run(10*time.Second, append(sessionArgs, "wait", "900")...)
			continue
		}
		if control == "" || selector == "" {
			break
		}
		if _, clickErr := run(15*time.Second, append(sessionArgs, "click", selector)...); clickErr != nil && !isBrowserEvaluationNavigationError(clickErr) {
			return state, fmt.Errorf("could not activate the %s playback control: %w", control, clickErr)
		}
		_, _ = run(10*time.Second, append(sessionArgs, "wait", fmt.Sprint(playbackControlWait(knowledge.Playback.Controls, control)))...)
	}
	state, lastErr = evaluateBrowserPlayback(run, sessionArgs)
	if lastErr != nil {
		return state, lastErr
	}
	if message := browserStringValue(state["error"]); message != "" {
		return state, fmt.Errorf("media did not start playing: %s", message)
	}
	return state, fmt.Errorf("media did not enter a verified playing state")
}

func inspectKnowledgePlaybackControl(run browserCommandRunner, sessionArgs []string, controls []siteknowledge.ControlHint) (string, string, bool, error) {
	if run == nil {
		return "", "", false, fmt.Errorf("browser playback runner is unavailable")
	}
	encoded, err := json.Marshal(controls)
	if err != nil {
		return "", "", false, fmt.Errorf("encode playback knowledge: %w", err)
	}
	script := fmt.Sprintf(`(()=>{const controls=%s;const visible=element=>Boolean(element&&(element.getClientRects().length>0||element.offsetParent));for(const hint of controls){for(const selector of hint.selectors||[]){let element;try{element=document.querySelector(selector)}catch(_){continue}if(!visible(element))continue;const labels=[element.getAttribute("aria-label"),element.getAttribute("title"),element.textContent].filter(Boolean).join(" ");const playing=(hint.playing_classes||[]).some(name=>element.classList.contains(name))||/(?:pause|暂停)/i.test(labels);element.setAttribute("data-athena-play-control",hint.id);return {found:true,playing,control:hint.id,selector:'[data-athena-play-control="'+CSS.escape(hint.id)+'"]'}}}return {found:false,playing:false,control:"",selector:""}})()`, string(encoded))
	output, err := run(20*time.Second, append(sessionArgs, "eval", script, "--json")...)
	if err != nil {
		return "", "", false, fmt.Errorf("could not inspect playback controls: %w", err)
	}
	state, ok := parseQQMusicPlaybackControl(output)
	if !ok {
		return "", "", false, fmt.Errorf("browser returned an unreadable playback control result")
	}
	found, _ := state["found"].(bool)
	if !found {
		return "", "", false, nil
	}
	return browserStringValue(state["control"]), browserStringValue(state["selector"]), boolMapState(state, "playing"), nil
}

func playbackControlWait(controls []siteknowledge.ControlHint, id string) int {
	for _, control := range controls {
		if control.ID == id && control.WaitMS > 0 {
			return control.WaitMS
		}
	}
	return 1000
}

func evaluateKnowledgePlayback(run browserCommandRunner, sessionArgs []string, hint *siteknowledge.PlayerStateHint) (map[string]any, error) {
	if hint == nil {
		return nil, fmt.Errorf("DOM player knowledge is unavailable")
	}
	encoded, err := json.Marshal(hint)
	if err != nil {
		return nil, fmt.Errorf("encode DOM player knowledge: %w", err)
	}
	script := fmt.Sprintf(`(async()=>{const hint=%s;const first=selectors=>{for(const selector of selectors||[]){try{const value=document.querySelector(selector);if(value)return value}catch(_){}}return null};const all=selector=>{if(!selector)return [];try{return [...document.querySelectorAll(selector)]}catch(_){return []}};const clock=value=>{const match=String(value||"").match(/(\d{1,2}):(\d{2})(?:\s*\/\s*(\d{1,2}):(\d{2}))?/);if(!match)return [0,0];return [Number(match[1])*60+Number(match[2]),match[3]!==undefined?Number(match[3])*60+Number(match[4]):0]};const read=()=>{const indicator=first(hint.playing_selectors);const rows=all(hint.item_selector).filter(row=>!hint.media_link_selector||row.querySelector(hint.media_link_selector));const activeIndex=rows.findIndex(row=>(hint.active_classes||[]).some(name=>row.classList.contains(name)));const activeRow=rows[activeIndex>=0?activeIndex:(rows.length===1?0:-1)];let mediaLink=null;try{mediaLink=activeRow&&hint.media_link_selector?activeRow.querySelector(hint.media_link_selector):null}catch(_){}let mediaURL="",mediaID="";if(mediaLink){try{const target=new URL(mediaLink.href,location.href);target.hash="";mediaURL=target.href;mediaID=target.searchParams.get("v")||target.pathname.split("/").filter(Boolean).pop()||""}catch(_){}}const mediaTitle=String(mediaLink&&(mediaLink.getAttribute("title")||mediaLink.getAttribute("aria-label")||mediaLink.textContent)||"").replace(/\s+/g," ").trim();const timeNode=first(hint.time_selectors);const values=clock(timeNode&&timeNode.textContent);return {playing:Boolean(indicator),current:values[0],duration:values[1],queueLength:rows.length,activeIndex,mediaID,mediaTitle,mediaURL}};const before=read();await new Promise(resolve=>setTimeout(resolve,850));const after=read();return {found:before.playing||after.playing||after.queueLength>0,playing:after.playing,paused:!after.playing,ended:false,kind:"dom_player",ready_state:after.playing?4:0,current_time:after.current,duration:after.duration,progressed:after.current>before.current,queue_length:after.queueLength,active_index:after.activeIndex,can_previous:after.queueLength>1&&(after.activeIndex<0||after.activeIndex>0),can_next:after.queueLength>1&&(after.activeIndex<0||after.activeIndex<after.queueLength-1),media_id:after.mediaID,media_title:after.mediaTitle,media_url:after.mediaURL,error:after.playing?"":"No active DOM player state was found",title:document.title||""}})()`, string(encoded))
	output, err := run(15*time.Second, append(sessionArgs, "eval", script, "--json")...)
	if err != nil {
		return nil, fmt.Errorf("could not inspect DOM player state: %w", err)
	}
	state, ok := parseBrowserPlaybackState(output)
	if !ok {
		return nil, fmt.Errorf("browser returned an unreadable DOM player result")
	}
	return state, nil
}

func boolMapState(state map[string]any, key string) bool {
	value, _ := state[key].(bool)
	return value
}

func parseQQMusicPlaybackControl(output string) (map[string]any, bool) {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &decoded) != nil {
		return nil, false
	}
	return findQQMusicPlaybackControl(decoded)
}

func findQQMusicPlaybackControl(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if _, hasFound := typed["found"]; hasFound {
			if _, hasControl := typed["control"]; hasControl {
				return typed, true
			}
		}
		for _, key := range []string{"data", "result", "value"} {
			if nested, exists := typed[key]; exists {
				if state, found := findQQMusicPlaybackControl(nested); found {
					return state, true
				}
			}
		}
	case []any:
		for _, item := range typed {
			if state, found := findQQMusicPlaybackControl(item); found {
				return state, true
			}
		}
	}
	return nil, false
}

func isBrowserEvaluationNavigationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"inspected target navigated or closed",
		"execution context was destroyed",
		"cannot find context with specified id",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func waitForBrowserPlayControls(run browserCommandRunner, sessionArgs []string, timeout time.Duration) (map[string]any, []browserSemanticElement, error) {
	deadline := time.Now().Add(timeout)
	var state map[string]any
	for {
		currentURL, _ := run(10*time.Second, append(sessionArgs, "get", "url")...)
		snapshot, err := run(15*time.Second, append(sessionArgs, "snapshot", "-i", "--urls", "-c", "-d", "6")...)
		if err != nil {
			return state, nil, err
		}
		state = map[string]any{
			"url":          currentURL,
			"snapshot":     snapshot,
			"key_elements": browserKeyElements(snapshot, 80),
		}
		if controls := browserPlayControls(state); len(controls) > 0 {
			return state, controls, nil
		}
		if time.Now().After(deadline) {
			return state, nil, nil
		}
		_, _ = run(10*time.Second, append(sessionArgs, "wait", "1000")...)
	}
}

func browserPlayControls(state map[string]any) []browserSemanticElement {
	controls := make([]browserSemanticElement, 0, 4)
	for _, element := range browserSemanticElements(state) {
		if isBrowserPlayButton(element) {
			controls = append(controls, element)
		}
	}
	sort.SliceStable(controls, func(left, right int) bool {
		return browserPlayControlPriority(controls[left]) < browserPlayControlPriority(controls[right])
	})
	return controls
}

func browserPlayControlPriority(element browserSemanticElement) int {
	title := strings.ToLower(strings.TrimSpace(browserElementTitle(element)))
	switch {
	case strings.Contains(title, "开始播放"), strings.Contains(title, "start playback"):
		return 0
	case strings.Contains(title, "继续播放"), strings.HasPrefix(title, "resume"):
		return 1
	default:
		return 2
	}
}

func resolveCurrentBrowserRef(run browserCommandRunner, sessionArgs []string, originalRef, targetLabel string) (string, error) {
	targetLabel = strings.TrimSpace(targetLabel)
	if targetLabel == "" {
		return originalRef, nil
	}
	snapshot, err := run(15*time.Second, append(sessionArgs, "snapshot", "-i", "--urls", "-c", "-d", "6")...)
	if err != nil {
		return "", fmt.Errorf("could not refresh browser page options: %w", err)
	}
	wanted := strings.ToLower(strings.Join(strings.Fields(targetLabel), " "))
	var matches []browserSemanticElement
	for _, element := range browserSemanticElements(map[string]any{"key_elements": browserKeyElements(snapshot, 80)}) {
		candidate := strings.ToLower(strings.Join(strings.Fields(browserElementTitle(element)), " "))
		if candidate == wanted {
			if element.Ref == originalRef {
				return element.Ref, nil
			}
			matches = append(matches, element)
		}
	}
	if len(matches) == 1 {
		return matches[0].Ref, nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("the page now contains multiple options named %q; refresh the options before choosing one", targetLabel)
	}
	return "", fmt.Errorf("the page option %q is no longer visible; refresh the options and retry", targetLabel)
}

func evaluateBrowserPlayback(run browserCommandRunner, sessionArgs []string) (map[string]any, error) {
	if run == nil {
		return nil, fmt.Errorf("browser playback runner is unavailable")
	}
	output, err := run(15*time.Second, append(sessionArgs, "eval", browserPlaybackScript, "--json")...)
	if err != nil {
		return nil, fmt.Errorf("could not inspect browser media playback: %w", err)
	}
	state, ok := parseBrowserPlaybackState(output)
	if !ok {
		return nil, fmt.Errorf("browser returned an unreadable playback result")
	}
	if browserPlaybackVerified(state) {
		return state, nil
	}
	currentURL, urlErr := run(10*time.Second, append(sessionArgs, "get", "url")...)
	if urlErr == nil {
		if knowledge, matched := siteknowledge.MatchURL(currentURL); matched && knowledge.Playback != nil && knowledge.Playback.Player != nil {
			if domState, domErr := evaluateKnowledgePlayback(run, sessionArgs, knowledge.Playback.Player); domErr == nil {
				return domState, nil
			}
		}
	}
	return state, nil
}

func parseBrowserPlaybackState(output string) (map[string]any, bool) {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &decoded) != nil {
		return nil, false
	}
	return findBrowserPlaybackState(decoded)
}

func findBrowserPlaybackState(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		_, hasFound := typed["found"]
		_, hasPlaying := typed["playing"]
		if hasFound && hasPlaying {
			return sanitizeBrowserPlaybackState(typed), true
		}
		for _, key := range []string{"data", "result", "value"} {
			if nested, ok := typed[key]; ok {
				if state, found := findBrowserPlaybackState(nested); found {
					return state, true
				}
			}
		}
	case []any:
		for _, item := range typed {
			if state, found := findBrowserPlaybackState(item); found {
				return state, true
			}
		}
	}
	return nil, false
}

func sanitizeBrowserPlaybackState(value map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{"found", "playing", "paused", "ended", "kind", "ready_state", "current_time", "duration", "progressed", "queue_length", "active_index", "can_previous", "can_next", "media_id", "media_title", "media_url", "error", "title"} {
		if item, ok := value[key]; ok {
			if number, numberOK := item.(float64); numberOK && (math.IsNaN(number) || math.IsInf(number, 0)) {
				continue
			}
			result[key] = item
		}
	}
	result["verified"] = browserPlaybackVerified(result)
	return result
}

func browserPlaybackVerified(state map[string]any) bool {
	found, _ := state["found"].(bool)
	playing, _ := state["playing"].(bool)
	if !found || !playing {
		return false
	}
	if browserStringValue(state["kind"]) == "media_session" {
		return true
	}
	progressed, _ := state["progressed"].(bool)
	return progressed || browserFloatValue(state["current_time"]) > 0 || browserFloatValue(state["ready_state"]) >= 2
}

func browserFloatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		result, _ := typed.Float64()
		return result
	default:
		return 0
	}
}

func enrichBrowserPlaybackIdentity(run browserCommandRunner, sessionArgs []string, state map[string]any) map[string]any {
	if state == nil || run == nil || browserStringValue(state["media_id"]) != "" {
		return state
	}
	currentURL, err := run(10*time.Second, append(sessionArgs, "get", "url")...)
	if err != nil {
		return state
	}
	kind, mediaID := browserMediaIdentity(currentURL)
	if mediaID == "" {
		return state
	}
	state["media_id"] = mediaID
	state["media_url"] = strings.TrimSpace(currentURL)
	if browserStringValue(state["kind"]) == "" || browserStringValue(state["kind"]) == "media_session" {
		state["kind"] = kind
	}
	return state
}

func validateBrowserPlaybackTarget(state map[string]any, expectedKind, expectedID, expectedLabel string) error {
	if expectedKind == "" || expectedID == "" {
		return nil
	}
	actualID := browserStringValue(state["media_id"])
	if actualID == "" {
		_, actualID = browserMediaIdentity(browserStringValue(state["media_url"]))
	}
	if actualID == "" {
		return fmt.Errorf("playback started, but the browser could not verify that it is the selected media %q", expectedLabel)
	}
	if actualID != expectedID {
		return fmt.Errorf("the page played %q instead of the selected media %q", browserStringValue(state["media_title"]), expectedLabel)
	}
	return nil
}

func isBrowserPlayButton(element browserSemanticElement) bool {
	title := strings.ToLower(strings.TrimSpace(browserElementTitle(element)))
	for _, expected := range []string{"play", "播放", "start playback", "开始播放", "resume", "继续播放"} {
		if title == expected {
			return true
		}
	}
	if strings.HasPrefix(title, "play ") || strings.HasPrefix(title, "resume ") || strings.HasPrefix(title, "start playback ") {
		return true
	}
	if strings.HasPrefix(title, "播放") && !strings.HasPrefix(title, "播放全部") && !strings.HasPrefix(title, "播放器") {
		return true
	}
	return false
}
