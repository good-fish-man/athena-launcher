package browser_runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const agentBrowserSocketDirectoryEnv = "AGENT_BROWSER_SOCKET_DIR"

func (b *browserController) browserCommand(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = browserCommandEnvironment(b.home)
	return command
}

func browserCommandEnvironment(home string) []string {
	environment := os.Environ()
	if runtime.GOOS == "windows" || strings.TrimSpace(os.Getenv(agentBrowserSocketDirectoryEnv)) != "" {
		return environment
	}
	root := "/tmp"
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		root = os.TempDir()
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(home)))
	directory := filepath.Join(root, "athena-browser-"+hex.EncodeToString(digest[:8]))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return environment
	}
	_ = os.Chmod(directory, 0o700)
	return append(environment, agentBrowserSocketDirectoryEnv+"="+directory)
}
