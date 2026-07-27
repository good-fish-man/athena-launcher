package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "athena-launcher:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "start"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	home, err := defaultHome()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	homeFlag := flags.String("home", home, "Athena data and installation directory")
	manifestFlag := flags.String("manifest", "", "release manifest file or HTTPS URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	absHome, err := filepath.Abs(*homeFlag)
	if err != nil {
		return err
	}
	manifestSource := strings.TrimSpace(*manifestFlag)
	if manifestSource == "" {
		manifestSource = defaultManifest(absHome)
	}
	opts := options{command: command, home: absHome, manifestSource: manifestSource}

	switch command {
	case "start":
		return startDetached(opts)
	case "run":
		return runForeground(opts)
	case "install", "update":
		_, _, _, err := prepare(context.Background(), opts)
		return err
	case "stop":
		return stopManaged(opts.home)
	case "status":
		printStatus(opts.home)
		return nil
	case "version":
		fmt.Printf("athena-launcher %s (%s)\n", launcherVersion, platformKey())
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func prepare(ctx context.Context, opts options) (*Manifest, *launcherState, map[string]string, error) {
	if err := os.MkdirAll(opts.home, 0o700); err != nil {
		return nil, nil, nil, err
	}
	state, err := loadState(opts.home)
	if err != nil {
		return nil, nil, nil, err
	}
	state.ManifestSource = opts.manifestSource
	manifest, err := loadManifest(ctx, opts.manifestSource)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := installDatabase(ctx, opts.home, manifest); err != nil {
		return nil, nil, nil, err
	}
	executables, err := installServices(ctx, opts.home, manifest, state)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := writeGeneratedConfigs(opts.home, state, executables); err != nil {
		return nil, nil, nil, err
	}
	return manifest, state, executables, nil
}

func runForeground(opts options) error {
	ctx, cancel := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer cancel()
	stopPath := filepath.Join(opts.home, "stop.request")
	_ = os.Remove(stopPath)
	go watchStopRequest(ctx, cancel, stopPath)

	manifest, state, executables, err := prepare(ctx, opts)
	if err != nil {
		return err
	}
	state.LauncherPID = os.Getpid()
	if err := saveState(opts.home, state); err != nil {
		return err
	}
	defer func() {
		state.LauncherPID = 0
		_ = saveState(opts.home, state)
	}()

	databaseDir := filepath.Join(opts.home, "postgres", manifest.Database.Version)
	database := newManagedDatabase(opts.home, databaseDir, manifest.Database.BinDir, state.DBPassword)
	fmt.Println("[postgres] preparing managed database")
	if err := database.Start(ctx); err != nil {
		return err
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer stopCancel()
		if err := database.Stop(stopCtx); err != nil {
			fmt.Fprintln(os.Stderr, "[postgres]", err)
		}
	}()
	fmt.Printf("[postgres] ready at 127.0.0.1:%d/%s\n", defaultDatabasePort, defaultDatabaseName)

	paths, err := writeGeneratedConfigs(opts.home, state, executables)
	if err != nil {
		return err
	}
	supervisor := newSupervisor(opts.home, paths, manifest, executables)
	if err := supervisor.StartAll(ctx); err != nil {
		return err
	}
	fmt.Printf("Athena is ready: http://127.0.0.1:%d\n", defaultClientHTTPPort)
	return supervisor.Run(ctx)
}

func startDetached(opts options) error {
	if healthyURL(fmt.Sprintf("http://127.0.0.1:%d/healthz", defaultClientHTTPPort)) {
		fmt.Printf("Athena is already running: http://127.0.0.1:%d\n", defaultClientHTTPPort)
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(opts.home, "logs"), 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(opts.home, "logs", "launcher.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(executable, "run", "--home", opts.home, "--manifest", opts.manifestSource)
	command.Stdout = logFile
	command.Stderr = logFile
	configureDetachedProcess(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	pid := command.Process.Pid
	_ = logFile.Close()
	_ = command.Process.Release()
	fmt.Printf("Athena startup initiated (pid=%d). First install may take several minutes.\n", pid)
	fmt.Printf("Logs: %s\n", logPath)
	return nil
}

func stopManaged(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	stopPath := filepath.Join(home, "stop.request")
	if err := os.WriteFile(stopPath, []byte(time.Now().Format(time.RFC3339Nano)), 0o600); err != nil {
		return err
	}
	state, _ := loadState(home)
	if state == nil || state.LauncherPID == 0 {
		fmt.Println("Stop requested; no managed launcher PID was recorded.")
		return nil
	}
	for attempt := 0; attempt < 40; attempt++ {
		if !healthyURL(fmt.Sprintf("http://127.0.0.1:%d/healthz", defaultClientHTTPPort)) {
			fmt.Println("Athena stopped.")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("launcher pid %d did not stop within 20 seconds; inspect %s", state.LauncherPID, filepath.Join(home, "logs", "launcher.log"))
}

func printStatus(home string) {
	state, _ := loadState(home)
	version := "not installed"
	pid := 0
	if state != nil {
		if state.Version != "" {
			version = state.Version
		}
		pid = state.LauncherPID
	}
	fmt.Printf("Version: %s\nLauncher PID: %d\n", version, pid)
	fmt.Printf("PostgreSQL: %s\n", statusLabel(tcpReachable(defaultDatabasePort)))
	fmt.Printf("agent-runtime: %s\n", statusLabel(healthyURL(fmt.Sprintf("http://127.0.0.1:%d/healthz", defaultRuntimeHTTPPort))))
	fmt.Printf("agent-runtime-client: %s\n", statusLabel(healthyURL(fmt.Sprintf("http://127.0.0.1:%d/healthz", defaultClientHTTPPort))))
}

func watchStopRequest(ctx context.Context, cancel context.CancelFunc, path string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				_ = os.Remove(path)
				cancel()
				return
			}
		}
	}
}

func healthyURL(address string) bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(address)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 500
}

func tcpReachable(port uint32) bool {
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), time.Second)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func statusLabel(ok bool) string {
	if ok {
		return "running"
	}
	return "stopped"
}

func printUsage() {
	fmt.Println(`Athena one-file installer and service manager

Usage:
  athena-launcher start   [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher run     [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher install [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher update  [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher stop    [--home PATH]
  athena-launcher status  [--home PATH]
  athena-launcher version

Environment:
  ATHENA_HOME          installation and data directory (default ~/.athena)
  ATHENA_MANIFEST_URL release manifest HTTPS URL or local file`)
}
