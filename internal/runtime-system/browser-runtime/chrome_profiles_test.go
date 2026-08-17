package browser_runtime

import (
	statepkg "athena-launcher/internal/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverChromeProfilesRecommendsLastUsedAccount(t *testing.T) {
	root := fakeChromeProfileRoot(t)
	t.Setenv(chromeUserDataDirEnv, root)

	catalog := discoverChromeProfiles()
	if catalog.Recommended != "Profile 2" {
		t.Fatalf("recommended profile = %q, want Profile 2", catalog.Recommended)
	}
	if len(catalog.Profiles) != 2 {
		t.Fatalf("profiles = %#v", catalog.Profiles)
	}
	selected := catalog.Profiles[0]
	if selected.Directory != "Profile 2" || selected.Name != "Work" || selected.Account != "work@example.com" || !selected.LastUsed {
		t.Fatalf("last used profile = %#v", selected)
	}
}

func TestApplyBrowserSettingsResolvesDisplayName(t *testing.T) {
	root := fakeChromeProfileRoot(t)
	t.Setenv(chromeUserDataDirEnv, root)
	state := &statepkg.State{}

	if err := applyBrowserSettings(state, browserSettingsRequest{Mode: browserAuthModeProfile, Profile: "Work"}); err != nil {
		t.Fatal(err)
	}
	if state.BrowserAuthMode != browserAuthModeProfile || state.BrowserProfile != "Profile 2" {
		t.Fatalf("browser settings = %q %q", state.BrowserAuthMode, state.BrowserProfile)
	}
}

func TestApplyBrowserSettingsRejectsUnknownProfileAtomically(t *testing.T) {
	root := fakeChromeProfileRoot(t)
	t.Setenv(chromeUserDataDirEnv, root)
	state := &statepkg.State{BrowserAuthMode: browserAuthModeIsolated}

	err := applyBrowserSettings(state, browserSettingsRequest{Mode: browserAuthModeProfile, Profile: "Missing"})
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("error = %v", err)
	}
	if state.BrowserAuthMode != browserAuthModeIsolated || state.BrowserProfile != "" {
		t.Fatalf("failed validation mutated state: %#v", state)
	}
}

func TestBrowserSettingsExposeDetectedProfilesAndSnapshotWarning(t *testing.T) {
	root := fakeChromeProfileRoot(t)
	t.Setenv(chromeUserDataDirEnv, root)
	settings := browserSettingsFromState(t.TempDir(), &statepkg.State{
		BrowserAuthMode: browserAuthModeProfile,
		BrowserProfile:  "Work",
	})

	if settings.Profile != "Profile 2" || settings.ProfileStatus != "ready" || len(settings.Profiles) != 2 {
		t.Fatalf("settings = %#v", settings)
	}
	if !containsBrowserSetting(settings.Warnings, "profile_snapshot") {
		t.Fatalf("warnings = %#v", settings.Warnings)
	}
}

func TestBrowserProfileUsesRecommendedProfileForEnvironmentOverride(t *testing.T) {
	root := fakeChromeProfileRoot(t)
	t.Setenv(chromeUserDataDirEnv, root)
	t.Setenv("ATHENA_BROWSER_AUTH_MODE", browserAuthModeProfile)

	if profile := browserProfileFromState(t.TempDir(), nil); profile != "Profile 2" {
		t.Fatalf("effective profile = %q, want Profile 2", profile)
	}
}

func fakeChromeProfileRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"Default", "Profile 2"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	data := []byte(`{"profile":{"last_used":"Profile 2","info_cache":{"Default":{"name":"Personal"},"Profile 2":{"name":"Work","user_name":"work@example.com"}}}}`)
	if err := os.WriteFile(filepath.Join(root, "Local State"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func containsBrowserSetting(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
