package browser_runtime

import (
	statepkg "athena-launcher/internal/state"
	"os"
	"path/filepath"
	"strings"
)

type browserSettingsRequest struct {
	Mode    string `json:"mode"`
	Profile string `json:"profile,omitempty"`
}

type browserSettingsResponse struct {
	Mode             string `json:"mode"`
	Profile          string `json:"profile,omitempty"`
	EffectiveProfile string `json:"effectiveProfile,omitempty"`
	EnvOverride      bool   `json:"envOverride"`
}

func browserSettingsFromState(home string, value *statepkg.State) browserSettingsResponse {
	state := value
	if state == nil {
		state = &statepkg.State{}
	}
	mode := normalizeBrowserAuthMode(state.BrowserAuthMode)
	envMode := strings.TrimSpace(os.Getenv("ATHENA_BROWSER_AUTH_MODE"))
	envOverride := envMode != ""
	if envOverride {
		mode = normalizeBrowserAuthMode(envMode)
	}
	return browserSettingsResponse{
		Mode:             mode,
		Profile:          strings.TrimSpace(state.BrowserProfile),
		EffectiveProfile: browserProfileFromState(home, state),
		EnvOverride:      envOverride,
	}
}

func applyBrowserSettings(state *statepkg.State, request browserSettingsRequest) {
	if state == nil {
		return
	}
	state.BrowserAuthMode = normalizeBrowserAuthMode(request.Mode)
	if state.BrowserAuthMode == browserAuthModeProfile {
		state.BrowserProfile = strings.TrimSpace(request.Profile)
	} else {
		state.BrowserProfile = ""
	}
}

func browserProfileFromState(home string, state *statepkg.State) string {
	for _, key := range []string{"ATHENA_BROWSER_PROFILE", "AGENT_BROWSER_PROFILE"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	if state != nil && normalizeBrowserAuthMode(state.BrowserAuthMode) == browserAuthModeProfile && strings.TrimSpace(state.BrowserProfile) != "" {
		return strings.TrimSpace(state.BrowserProfile)
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "browser", "profiles", "default")
}
