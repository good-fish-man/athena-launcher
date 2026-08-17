package deployment

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const (
	connectionModeLocal  = "local"
	connectionModeRemote = "remote"
)

type deploymentSelection struct {
	Mode      string `json:"mode"`
	RemoteURL string `json:"remoteUrl,omitempty"`
	Token     string `json:"deviceToken,omitempty"`
}

func normalizeDeployment(selection deploymentSelection) (deploymentSelection, error) {
	selection.Mode = strings.ToLower(strings.TrimSpace(selection.Mode))
	selection.RemoteURL = strings.TrimRight(strings.TrimSpace(selection.RemoteURL), "/")
	selection.Token = strings.TrimSpace(selection.Token)
	switch selection.Mode {
	case connectionModeLocal:
		selection.RemoteURL = ""
		selection.Token = ""
		return selection, nil
	case connectionModeRemote:
		if selection.RemoteURL == "" {
			return deploymentSelection{}, fmt.Errorf("remote agent-runtime-client URL is required")
		}
	default:
		return deploymentSelection{}, fmt.Errorf("connection mode must be local or remote")
	}

	parsed, err := url.Parse(selection.RemoteURL)
	if err != nil || parsed.Host == "" {
		return deploymentSelection{}, fmt.Errorf("remote agent-runtime-client URL is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return deploymentSelection{}, fmt.Errorf("remote URL cannot contain credentials, query parameters, or fragments")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return deploymentSelection{}, fmt.Errorf("remote URL must use HTTPS; HTTP is allowed only for localhost")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{"/api/agent-runtime-client/v1", "/healthz"} {
		if strings.HasSuffix(parsed.Path, suffix) {
			parsed.Path = strings.TrimSuffix(parsed.Path, suffix)
			break
		}
	}
	selection.RemoteURL = strings.TrimRight(parsed.String(), "/")
	return selection, nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "wails.localhost") {
		return true
	}
	return net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func savedDeploymentSelection(state *launcherState) (deploymentSelection, error) {
	if state == nil || state.ConnectionMode == "" {
		return deploymentSelection{Mode: connectionModeLocal}, nil
	}
	return normalizeDeployment(deploymentSelection{Mode: state.ConnectionMode, RemoteURL: state.RemoteClientURL, Token: state.RemoteDeviceToken})
}

func deploymentFromState(state *launcherState) deploymentSelection {
	if state == nil {
		return deploymentSelection{Mode: connectionModeLocal}
	}
	selection, err := normalizeDeployment(deploymentSelection{Mode: state.ConnectionMode, RemoteURL: state.RemoteClientURL, Token: state.RemoteDeviceToken})
	if err == nil {
		return selection
	}
	return deploymentSelection{Mode: connectionModeLocal}
}

func deploymentSelectionRequired(state *launcherState, stateErr, selectionErr error) bool {
	return stateErr != nil || selectionErr != nil || state == nil || state.ConnectionMode == "" || !state.DeploymentConfigured
}
