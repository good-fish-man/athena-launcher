package deployment

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type processExit struct {
	name string
	err  error
}

type managedProcess struct {
	spec ServiceSpec
	path string
	cmd  *exec.Cmd
	done chan struct{}
}

type supervisor struct {
	home                   string
	paths                  *generatedPaths
	manifest               *Manifest
	executables            map[string]string
	processes              map[string]*managedProcess
	exits                  chan processExit
	stopping               bool
	tracker                *startupTracker
	browserDataDir         string
	browserEncryptionKey   string
	internalServiceToken   string
	bootstrapAdminPassword string
}

func newSupervisor(home string, paths *generatedPaths, manifest *Manifest, executables map[string]string, tracker *startupTracker, browserDataDir, browserEncryptionKey, internalServiceToken, bootstrapAdminPassword string) *supervisor {
	return &supervisor{home: home, paths: paths, manifest: manifest, executables: executables, processes: make(map[string]*managedProcess), exits: make(chan processExit, len(manifest.Services)*2), tracker: tracker, browserDataDir: browserDataDir, browserEncryptionKey: browserEncryptionKey, internalServiceToken: internalServiceToken, bootstrapAdminPassword: bootstrapAdminPassword}
}

func (s *supervisor) StartAll(ctx context.Context) error {
	if err := prepareManagedServicePorts(ctx, s.home, s.manifest); err != nil {
		return err
	}
	for _, spec := range s.manifest.Services {
		if err := s.start(ctx, spec); err != nil {
			s.StopAll()
			return err
		}
	}
	return nil
}

func (s *supervisor) Run(ctx context.Context, control *startupController, checkUpdates func() ([]packageUpdate, error)) error {
	for {
		select {
		case <-ctx.Done():
			s.stopping = true
			s.StopAll()
			return nil
		case <-control.checkUpdate:
			s.tracker.checkingForUpdates()
			updates, err := checkUpdates()
			if err != nil {
				s.tracker.updateError(err)
				continue
			}
			if len(updates) == 0 {
				s.tracker.clearUpdate("All installed packages are current")
				continue
			}
			s.tracker.offerUpdate(updates, true)
		case <-control.dismissUpdate:
			s.tracker.clearUpdate("Update postponed")
		case <-control.applyUpdate:
			if s.tracker.current().Update.State != "available" && s.tracker.current().Update.State != "applying" {
				continue
			}
			s.tracker.protectingUpdate()
			// The caller owns PostgreSQL and creates the recovery point only after
			// every managed process and the database have stopped cleanly.
			return errUpdateRequested
		case event := <-s.exits:
			if s.stopping {
				continue
			}
			fmt.Printf("[%s] exited: %v; restarting in 2 seconds\n", event.name, event.err)
			s.tracker.begin(event.name, fmt.Sprintf("%s exited; restarting", event.name))
			time.Sleep(2 * time.Second)
			spec, ok := s.service(event.name)
			if !ok {
				return fmt.Errorf("unknown exited service %s", event.name)
			}
			if err := s.start(ctx, spec); err != nil {
				return fmt.Errorf("restart %s: %w", event.name, err)
			}
			if s.tracker != nil {
				snapshot := s.tracker.current()
				s.tracker.ready(snapshot.FrontendURL)
			}
		}
	}
}

func (s *supervisor) start(ctx context.Context, spec ServiceSpec) (returnErr error) {
	s.tracker.begin(spec.Name, "Starting "+spec.Name)
	defer func() {
		if returnErr != nil {
			s.tracker.fail(spec.Name, returnErr)
		}
	}()
	executable := s.executables[spec.Name]
	if executable == "" {
		return fmt.Errorf("service %s is not installed", spec.Name)
	}
	values := map[string]string{"{home}": s.home, "{config}": filepath.Dir(s.paths.clientConfig), "{install}": filepath.Dir(executable)}
	args := expandValues(spec.Args, values)
	env := append([]string{}, os.Environ()...)
	if runtime.GOOS == "darwin" {
		env = setEnvironmentValue(env, "PATH", macOSServicePath(os.Getenv("PATH")))
	}
	for key, value := range spec.Env {
		env = setEnvironmentValue(env, key, expandValue(value, values))
	}
	env = managedBrowserEnvironment(env, effectiveManagedBrowserDataDir(s.browserDataDir), s.browserEncryptionKey)
	if s.internalServiceToken != "" {
		env = setEnvironmentValue(env, "ATHENA_INTERNAL_SERVICE_TOKEN", s.internalServiceToken)
	}
	if browserExecutable := s.executables["agent-browser"]; browserExecutable != "" {
		env = setEnvironmentValue(env, "ATHENA_AGENT_BROWSER_BIN", browserExecutable)
	}
	switch spec.Name {
	case "agent-runtime":
		env = append(env, "AGENT_RUNTIME_CONFIG="+s.paths.runtimeConfig)
	case "agent-runtime-client":
		if s.bootstrapAdminPassword != "" {
			env = setEnvironmentValue(env, "ATHENA_BOOTSTRAP_ADMIN_USERNAME", "athena")
			env = setEnvironmentValue(env, "ATHENA_BOOTSTRAP_ADMIN_PASSWORD", s.bootstrapAdminPassword)
		}
		if len(args) == 0 {
			args = []string{"--config", s.paths.clientConfig}
		}
	}
	logPath := filepath.Join(s.home, "logs", spec.Name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, args...)
	cmd.Dir = filepath.Dir(executable)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start %s: %w", spec.Name, err)
	}
	_ = logFile.Close()
	process := &managedProcess{spec: spec, path: executable, cmd: cmd, done: make(chan struct{})}
	s.processes[spec.Name] = process
	go func() {
		err := cmd.Wait()
		close(process.done)
		s.exits <- processExit{name: spec.Name, err: err}
	}()
	fmt.Printf("[%s] started pid=%d\n", spec.Name, cmd.Process.Pid)
	if spec.HealthURL != "" {
		healthURL := expandValue(spec.HealthURL, values)
		if err := waitHTTP(ctx, healthURL, process.done, 90*time.Second); err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("%s health check: %w (log: %s)", spec.Name, err, logPath)
		}
		fmt.Printf("[%s] healthy at %s\n", spec.Name, healthURL)
		s.tracker.complete(spec.Name, "Healthy at "+healthURL)
	} else {
		s.tracker.complete(spec.Name, fmt.Sprintf("Started with process ID %d", cmd.Process.Pid))
	}
	return nil
}

func macOSServicePath(current string) string {
	paths := []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/local/sbin"}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		seen[path] = true
	}
	for _, path := range filepath.SplitList(current) {
		if path != "" && !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	return strings.Join(paths, string(os.PathListSeparator))
}

func setEnvironmentValue(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}

func effectiveManagedBrowserDataDir(configured string) string {
	for _, key := range []string{"ATHENA_AGENT_BROWSER_HOME", "AGENT_BROWSER_HOME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return strings.TrimSpace(configured)
}

func managedBrowserEnvironment(env []string, dataDir, encryptionKey string) []string {
	if dataDir = strings.TrimSpace(dataDir); dataDir != "" {
		env = setEnvironmentValue(env, "AGENT_BROWSER_HOME", dataDir)
		env = setEnvironmentValue(env, "ATHENA_AGENT_BROWSER_HOME", dataDir)
	}
	if encryptionKey = strings.TrimSpace(encryptionKey); encryptionKey != "" {
		env = setEnvironmentValue(env, "AGENT_BROWSER_ENCRYPTION_KEY", encryptionKey)
	}
	return env
}

func (s *supervisor) StopAll() {
	if s == nil || s.manifest == nil {
		return
	}
	for i := len(s.manifest.Services) - 1; i >= 0; i-- {
		name := s.manifest.Services[i].Name
		process := s.processes[name]
		if process == nil || process.cmd == nil || process.cmd.Process == nil {
			continue
		}
		if err := process.cmd.Process.Signal(os.Interrupt); err != nil {
			fmt.Printf("[%s] graceful stop signal failed pid=%d err=%v\n", name, process.cmd.Process.Pid, err)
		}
		select {
		case <-process.done:
		case <-time.After(10 * time.Second):
			fmt.Printf("[%s] graceful stop timed out pid=%d; forcing termination\n", name, process.cmd.Process.Pid)
			if err := process.cmd.Process.Kill(); err != nil {
				fmt.Printf("[%s] force stop failed pid=%d err=%v\n", name, process.cmd.Process.Pid, err)
			}
			<-process.done
		}
		delete(s.processes, name)
	}
}

func (s *supervisor) service(name string) (ServiceSpec, bool) {
	for _, spec := range s.manifest.Services {
		if spec.Name == name {
			return spec, true
		}
	}
	return ServiceSpec{}, false
}

func waitHTTP(ctx context.Context, address string, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return fmt.Errorf("process exited before becoming healthy")
		case <-deadline.C:
			return fmt.Errorf("timed out after %s", timeout)
		case <-ticker.C:
			resp, err := client.Get(address)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 500 {
					return nil
				}
			}
		}
	}
}

func expandValues(values []string, replacements map[string]string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = expandValue(value, replacements)
	}
	return out
}

func expandValue(value string, replacements map[string]string) string {
	for token, replacement := range replacements {
		value = strings.ReplaceAll(value, token, replacement)
	}
	return value
}
