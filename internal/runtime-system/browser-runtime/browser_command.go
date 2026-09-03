package browser_runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	agentBrowserSocketDirectoryEnv = "AGENT_BROWSER_SOCKET_DIR"
	agentBrowserIdleTimeoutEnv     = "AGENT_BROWSER_IDLE_TIMEOUT_MS"
	agentBrowserHomeEnv            = "AGENT_BROWSER_HOME"
	athenaAgentBrowserHomeEnv      = "ATHENA_AGENT_BROWSER_HOME"
	agentBrowserEncryptionKeyEnv   = "AGENT_BROWSER_ENCRYPTION_KEY"
	// Keep managed sessions alive across normal conversational follow-ups.
	managedBrowserIdleTimeout = "1800000"
)

func (b *browserController) browserCommand(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = configuredBrowserCommandEnvironment(b.home, b.dataDir, b.encryptionKey)
	return command
}

func browserCommandEnvironment(home string) []string {
	return configuredBrowserCommandEnvironment(home, "", "")
}

func configuredBrowserCommandEnvironment(home, dataDir, encryptionKey string) []string {
	environment := os.Environ()
	if dataDir = effectiveAgentBrowserDataDir(dataDir); dataDir != "" {
		environment = setBrowserEnvironmentValue(environment, agentBrowserHomeEnv, dataDir)
		environment = setBrowserEnvironmentValue(environment, athenaAgentBrowserHomeEnv, dataDir)
	}
	if encryptionKey = strings.TrimSpace(encryptionKey); encryptionKey != "" {
		environment = setBrowserEnvironmentValue(environment, agentBrowserEncryptionKeyEnv, encryptionKey)
	}
	if browserAuthMode(home) != browserAuthModeAutoConnect && strings.TrimSpace(os.Getenv(agentBrowserIdleTimeoutEnv)) == "" {
		environment = setBrowserEnvironmentValue(environment, agentBrowserIdleTimeoutEnv, managedBrowserIdleTimeout)
	}
	if runtime.GOOS == "windows" || strings.TrimSpace(os.Getenv(agentBrowserSocketDirectoryEnv)) != "" {
		return environment
	}
	directory, ok := agentBrowserSocketDirectory(home)
	if !ok {
		return environment
	}
	return append(environment, agentBrowserSocketDirectoryEnv+"="+directory)
}

func effectiveAgentBrowserDataDir(configured string) string {
	for _, key := range []string{athenaAgentBrowserHomeEnv, agentBrowserHomeEnv} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return strings.TrimSpace(configured)
}

func setBrowserEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}

func agentBrowserSocketDirectory(home string) (string, bool) {
	if directory := strings.TrimSpace(os.Getenv(agentBrowserSocketDirectoryEnv)); directory != "" {
		return directory, true
	}
	if runtime.GOOS == "windows" {
		return "", false
	}
	root := "/tmp"
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		root = os.TempDir()
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(home)))
	directory := filepath.Join(root, "athena-browser-"+hex.EncodeToString(digest[:8]))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", false
	}
	_ = os.Chmod(directory, 0o700)
	return directory, true
}

func (b *browserController) managedBrowserDaemonPID(sessionID string) (int, error) {
	if browserAuthMode(b.home) == browserAuthModeAutoConnect {
		return 0, nil
	}
	if !browserSessionPattern.MatchString(strings.TrimSpace(sessionID)) {
		return 0, fmt.Errorf("invalid browser session id")
	}
	directory, ok := agentBrowserSocketDirectory(b.home)
	if !ok {
		return 0, nil
	}
	data, err := os.ReadFile(filepath.Join(directory, "namespaces", "athena", "run", sessionID+".pid"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read managed browser daemon pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 0, fmt.Errorf("invalid managed browser daemon pid")
	}
	return pid, nil
}

func (b *browserController) stopManagedBrowserDaemon(sessionID string, expectedPID int) error {
	if expectedPID <= 1 || browserAuthMode(b.home) == browserAuthModeAutoConnect {
		return nil
	}
	currentPID, err := b.managedBrowserDaemonPID(sessionID)
	if err != nil {
		return err
	}
	if currentPID == 0 {
		return nil
	}
	if currentPID != expectedPID {
		return fmt.Errorf("managed browser daemon pid changed during shutdown")
	}
	process, err := os.FindProcess(expectedPID)
	if err != nil {
		return fmt.Errorf("find managed browser daemon: %w", err)
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) && !processAlreadyExited(err) {
		return fmt.Errorf("stop managed browser daemon: %w", err)
	}
	b.cleanupManagedBrowserDaemonFiles(sessionID)
	return nil
}

func (b *browserController) cleanupManagedBrowserDaemonFiles(sessionID string) {
	directory, ok := agentBrowserSocketDirectory(b.home)
	if !ok || !browserSessionPattern.MatchString(strings.TrimSpace(sessionID)) {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(directory, "namespaces", "athena", "run", sessionID+".*"))
	for _, path := range matches {
		_ = os.Remove(path)
	}
}

func processAlreadyExited(err error) bool {
	detail := strings.ToLower(err.Error())
	return strings.Contains(detail, "no such process") || strings.Contains(detail, "process already finished")
}
