package browser_runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const chromeUserDataDirEnv = "ATHENA_CHROME_USER_DATA_DIR"

type browserProfileOption struct {
	Directory string `json:"directory"`
	Name      string `json:"name"`
	Account   string `json:"account,omitempty"`
	LastUsed  bool   `json:"lastUsed"`
}

type chromeProfileCatalog struct {
	Profiles    []browserProfileOption
	Recommended string
}

type chromeLocalState struct {
	Profile struct {
		LastUsed string                            `json:"last_used"`
		Info     map[string]chromeLocalProfileInfo `json:"info_cache"`
	} `json:"profile"`
}

type chromeLocalProfileInfo struct {
	Name     string `json:"name"`
	UserName string `json:"user_name"`
	GAIAName string `json:"gaia_name"`
}

func discoverChromeProfiles() chromeProfileCatalog {
	for _, root := range chromeUserDataCandidates() {
		catalog, ok := readChromeProfileCatalog(root)
		if ok {
			return catalog
		}
	}
	return chromeProfileCatalog{Profiles: []browserProfileOption{}}
}

func chromeUserDataCandidates() []string {
	var candidates []string
	if override := strings.TrimSpace(os.Getenv(chromeUserDataDirEnv)); override != "" {
		return []string{expandBrowserProfilePath(override)}
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates,
			filepath.Join(home, "Library", "Application Support", "Google", "Chrome"),
			filepath.Join(home, "Library", "Application Support", "Chromium"),
		)
	case "windows":
		localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if localAppData != "" {
			candidates = append(candidates,
				filepath.Join(localAppData, "Google", "Chrome", "User Data"),
				filepath.Join(localAppData, "Chromium", "User Data"),
			)
		}
	default:
		candidates = append(candidates,
			filepath.Join(home, ".config", "google-chrome"),
			filepath.Join(home, ".config", "chromium"),
			filepath.Join(home, "snap", "chromium", "common", "chromium"),
		)
	}
	return uniqueNonEmptyPaths(candidates)
}

func readChromeProfileCatalog(root string) (chromeProfileCatalog, bool) {
	data, err := os.ReadFile(filepath.Join(root, "Local State"))
	if err != nil {
		return chromeProfileCatalog{}, false
	}
	var state chromeLocalState
	if err := json.Unmarshal(data, &state); err != nil || len(state.Profile.Info) == 0 {
		return chromeProfileCatalog{}, false
	}
	profiles := make([]browserProfileOption, 0, len(state.Profile.Info))
	for directory, info := range state.Profile.Info {
		directory = strings.TrimSpace(directory)
		if directory == "" || !directoryExists(filepath.Join(root, directory)) {
			continue
		}
		name := strings.TrimSpace(info.Name)
		if name == "" {
			name = directory
		}
		account := strings.TrimSpace(info.UserName)
		if account == "" {
			account = strings.TrimSpace(info.GAIAName)
		}
		profiles = append(profiles, browserProfileOption{
			Directory: directory,
			Name:      name,
			Account:   account,
			LastUsed:  directory == strings.TrimSpace(state.Profile.LastUsed),
		})
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		if profiles[i].LastUsed != profiles[j].LastUsed {
			return profiles[i].LastUsed
		}
		if (profiles[i].Account != "") != (profiles[j].Account != "") {
			return profiles[i].Account != ""
		}
		return strings.ToLower(profiles[i].Name) < strings.ToLower(profiles[j].Name)
	})
	if len(profiles) == 0 {
		return chromeProfileCatalog{}, false
	}
	recommended := profiles[0].Directory
	return chromeProfileCatalog{Profiles: profiles, Recommended: recommended}, true
}

func (c chromeProfileCatalog) resolve(value string) (browserProfileOption, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = c.Recommended
	}
	for _, profile := range c.Profiles {
		if profile.Directory == value {
			return profile, true, nil
		}
	}
	lower := strings.ToLower(value)
	var nameMatches []browserProfileOption
	for _, profile := range c.Profiles {
		if strings.ToLower(profile.Name) == lower {
			nameMatches = append(nameMatches, profile)
		}
	}
	if len(nameMatches) == 1 {
		return nameMatches[0], true, nil
	}
	if len(nameMatches) > 1 {
		return browserProfileOption{}, false, fmt.Errorf("Chrome profile name %q is ambiguous; choose its directory name", value)
	}
	for _, profile := range c.Profiles {
		if strings.ToLower(profile.Directory) == lower {
			return profile, true, nil
		}
	}
	return browserProfileOption{}, false, nil
}

func isBrowserProfilePath(value string) bool {
	value = strings.TrimSpace(value)
	return filepath.IsAbs(value) || strings.HasPrefix(value, "~") || strings.ContainsAny(value, `/\\`)
}

func expandBrowserProfilePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			value = filepath.Join(home, strings.TrimLeft(value[1:], `/\`))
		}
	}
	return filepath.Clean(value)
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func uniqueNonEmptyPaths(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "." {
			continue
		}
		key := filepath.Clean(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}
