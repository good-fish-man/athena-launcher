package browser_runtime

import (
	statepkg "athena-launcher/internal/state"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type browserSettingsRequest struct {
	Mode    string `json:"mode"`
	Profile string `json:"profile,omitempty"`
}

type browserSettingsResponse struct {
	Mode               string                 `json:"mode"`
	Profile            string                 `json:"profile,omitempty"`
	EffectiveProfile   string                 `json:"effectiveProfile,omitempty"`
	RecommendedProfile string                 `json:"recommendedProfile,omitempty"`
	ProfileStatus      string                 `json:"profileStatus"`
	Profiles           []browserProfileOption `json:"profiles"`
	Warnings           []string               `json:"warnings,omitempty"`
	EnvOverride        bool                   `json:"envOverride"`
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
	catalog := discoverChromeProfiles()
	profile := strings.TrimSpace(state.BrowserProfile)
	status, warnings := browserProfileStatus(mode, profile, catalog)
	effectiveProfile := browserProfileFromState(home, state)
	if mode == browserAuthModeProfile && !isBrowserProfilePath(profile) {
		if selected, ok, _ := catalog.resolve(profile); ok {
			profile = selected.Directory
			effectiveProfile = selected.Directory
		}
	}
	return browserSettingsResponse{
		Mode:               mode,
		Profile:            profile,
		EffectiveProfile:   effectiveProfile,
		RecommendedProfile: catalog.Recommended,
		ProfileStatus:      status,
		Profiles:           catalog.Profiles,
		Warnings:           warnings,
		EnvOverride:        envOverride,
	}
}

func applyBrowserSettings(state *statepkg.State, request browserSettingsRequest) error {
	if state == nil {
		return fmt.Errorf("browser settings state is required")
	}
	mode := normalizeBrowserAuthMode(request.Mode)
	if mode == browserAuthModeProfile {
		profile := strings.TrimSpace(request.Profile)
		if isBrowserProfilePath(profile) {
			profile = expandBrowserProfilePath(profile)
			if !directoryExists(profile) {
				return fmt.Errorf("Chrome profile directory does not exist: %s", profile)
			}
			state.BrowserAuthMode = mode
			state.BrowserProfile = profile
			return nil
		}
		catalog := discoverChromeProfiles()
		if len(catalog.Profiles) == 0 {
			return fmt.Errorf("no Chrome profiles were found; open regular Chrome once, then refresh the profile list")
		}
		resolved, ok, err := catalog.resolve(profile)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("Chrome profile %q was not found on this computer", profile)
		}
		state.BrowserAuthMode = mode
		state.BrowserProfile = resolved.Directory
	} else {
		state.BrowserAuthMode = mode
		state.BrowserProfile = ""
	}
	return nil
}

func browserProfileFromState(home string, state *statepkg.State) string {
	for _, key := range []string{"ATHENA_BROWSER_PROFILE", "AGENT_BROWSER_PROFILE"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	mode := browserAuthModeIsolated
	if state != nil {
		mode = normalizeBrowserAuthMode(state.BrowserAuthMode)
	}
	if override := strings.TrimSpace(os.Getenv("ATHENA_BROWSER_AUTH_MODE")); override != "" {
		mode = normalizeBrowserAuthMode(override)
	}
	if mode == browserAuthModeProfile {
		if state != nil {
			if profile := strings.TrimSpace(state.BrowserProfile); profile != "" {
				return profile
			}
		}
		return discoverChromeProfiles().Recommended
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "browser", "profiles", "default")
}

func browserProfileStatus(mode, profile string, catalog chromeProfileCatalog) (string, []string) {
	if mode != browserAuthModeProfile {
		return "not_applicable", nil
	}
	warnings := []string{"profile_snapshot"}
	if runtime.GOOS == "windows" {
		warnings = append(warnings, "windows_cookie_encryption")
	}
	if isBrowserProfilePath(profile) {
		if directoryExists(expandBrowserProfilePath(profile)) {
			return "custom_path", warnings
		}
		return "not_found", append(warnings, "profile_not_found")
	}
	if len(catalog.Profiles) == 0 {
		return "no_profiles", append(warnings, "no_profiles")
	}
	selected, ok, _ := catalog.resolve(profile)
	if !ok {
		return "not_found", append(warnings, "profile_not_found")
	}
	if selected.Account == "" {
		return "account_unknown", append(warnings, "profile_account_not_detected")
	}
	return "ready", warnings
}
