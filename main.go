package main

import (
	"context"
	"errors"
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
	case "launch":
		return launchDesktop(opts)
	case "start":
		return startDetached(opts)
	case "run":
		return runForeground(opts)
	case "install", "update":
		_, _, _, err := prepare(context.Background(), opts)
		return err
	case "validate":
		manifest, err := loadManifest(context.Background(), opts.manifestSource)
		if err != nil {
			return err
		}
		fmt.Printf("Manifest %s is valid for %s (%d services).\n", manifest.Version, platformKey(), len(manifest.Services))
		return nil
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

func launchDesktop(opts options) error {
	frontendAddress := fmt.Sprintf("http://127.0.0.1:%d/", defaultFrontendPort)
	startupAddress := fmt.Sprintf("http://127.0.0.1:%d/", defaultStartupPort)
	if startupCenterHealthy() {
		requestStartupUpdateCheck()
		return openBrowser(startupAddress)
	}
	if healthyURL(frontendAddress) {
		return openBrowser(frontendAddress)
	}
	if err := startDetached(opts); err != nil {
		return err
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if startupCenterHealthy() {
			return openBrowser(startupAddress)
		}
		if healthyURL(frontendAddress) {
			return openBrowser(frontendAddress)
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("Athena startup center did not open within 30 seconds; inspect %s", filepath.Join(opts.home, "logs", "launcher.log"))
		case <-ticker.C:
		}
	}
}

func prepare(ctx context.Context, opts options) (*Manifest, *launcherState, map[string]string, error) {
	return prepareWithTracker(ctx, opts, nil, nil, true)
}

func prepareWithTracker(ctx context.Context, opts options, tracker *startupTracker, control *startupController, updateApproved bool) (*Manifest, *launcherState, map[string]string, error) {
	if err := os.MkdirAll(opts.home, 0o700); err != nil {
		return nil, nil, nil, err
	}
	tracker.begin("manifest", "Reading release manifest")
	state, err := loadState(opts.home)
	if err != nil {
		tracker.fail("manifest", err)
		return nil, nil, nil, err
	}
	state.ManifestSource = opts.manifestSource
	manifest, err := loadManifest(ctx, opts.manifestSource)
	if err != nil {
		tracker.fail("manifest", err)
		return nil, nil, nil, err
	}
	tracker.complete("manifest", fmt.Sprintf("Release %s verified for %s", manifest.Version, platformKey()))
	if updates := checkPackageUpdates(opts.home, manifest); len(updates) > 0 && control != nil && !updateApproved {
		tracker.offerUpdate(updates, false)
		select {
		case <-ctx.Done():
			return nil, nil, nil, ctx.Err()
		case <-control.applyUpdate:
			tracker.applyingUpdate()
		}
	}

	tracker.begin("database-package", "Checking the managed PostgreSQL package")
	if _, err := installDatabase(ctx, opts.home, manifest); err != nil {
		tracker.fail("database-package", err)
		return nil, nil, nil, err
	}
	tracker.complete("database-package", fmt.Sprintf("PostgreSQL %s is available", manifest.Database.Version))

	tracker.begin("services-package", "Checking Agent Runtime packages")
	executables, err := installServices(ctx, opts.home, manifest, state)
	if err != nil {
		tracker.fail("services-package", err)
		return nil, nil, nil, err
	}
	tracker.complete("services-package", fmt.Sprintf("%d runtime packages are available", len(executables)))

	tracker.begin("frontend-package", "Checking the Athena interface package")
	if _, err := installFrontend(ctx, opts.home, manifest, state); err != nil {
		tracker.fail("frontend-package", err)
		return nil, nil, nil, err
	}
	tracker.complete("frontend-package", "Athena interface package is available")

	tracker.begin("configuration", "Generating local service configuration")
	if _, err := writeGeneratedConfigs(opts.home, state, executables); err != nil {
		tracker.fail("configuration", err)
		return nil, nil, nil, err
	}
	tracker.complete("configuration", "Local service configuration is ready")
	if err := saveInstalledManifest(opts.home, manifest); err != nil {
		tracker.fail("configuration", err)
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
	tracker := newStartupTracker(opts.home)
	control := newStartupController()
	statusServer, err := startStartupServer(tracker, control)
	if err != nil {
		return err
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		_ = statusServer.Stop(stopCtx)
	}()

	updateApproved := false
	for {
		err = runManaged(ctx, opts, tracker, control, updateApproved)
		if err == nil {
			return nil
		}
		if errors.Is(err, errUpdateRequested) {
			updateApproved = true
			tracker.reset()
			tracker.applyingUpdate()
			continue
		}
		if tracker.current().State != "error" {
			tracker.fail("", err)
		}
		fmt.Fprintln(os.Stderr, "[startup]", err)
		select {
		case <-ctx.Done():
			return err
		case <-statusServer.retry:
			tracker.reset()
			continue
		case <-time.After(30 * time.Minute):
			return err
		}
	}
}

func runManaged(ctx context.Context, opts options, tracker *startupTracker, control *startupController, updateApproved bool) error {
	manifest, state, executables, err := prepareWithTracker(ctx, opts, tracker, control, updateApproved)
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

	databaseDir, err := installDatabase(ctx, opts.home, manifest)
	if err != nil {
		tracker.fail("database", err)
		return err
	}
	database := newManagedDatabase(opts.home, databaseDir, manifest.Database.BinDir, state.DBPassword)
	fmt.Println("[postgres] preparing managed database")
	tracker.begin("database", "Starting the managed PostgreSQL database")
	if err := database.Start(ctx); err != nil {
		tracker.fail("database", err)
		return err
	}
	tracker.complete("database", fmt.Sprintf("PostgreSQL is ready on port %d", defaultDatabasePort))
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
	supervisor := newSupervisor(opts.home, paths, manifest, executables, tracker)
	if err := supervisor.StartAll(ctx); err != nil {
		return err
	}
	defer supervisor.StopAll()
	frontendPath := ""
	if manifest.Frontend != nil {
		frontendVersion := state.Installed["frontend"]
		if frontendVersion == "" {
			frontendVersion = manifest.Version
		}
		frontendPath = frontendRoot(filepath.Join(opts.home, "frontend", frontendVersion), manifest.Frontend.Root)
	}
	tracker.begin("frontend", "Starting the Athena interface")
	frontend, err := startFrontendServer(manifest, frontendPath)
	if err != nil {
		tracker.fail("frontend", err)
		return err
	}
	frontendURL := fmt.Sprintf("http://127.0.0.1:%d/", defaultFrontendPort)
	if frontend != nil {
		frontendURL = "http://" + frontend.address + "/"
		tracker.complete("frontend", "Athena interface is accepting connections")
	} else {
		tracker.complete("frontend", "API-only mode is ready")
	}
	tracker.clearUpdate("All installed packages are current")
	tracker.ready(frontendURL)
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := frontend.Stop(stopCtx); err != nil {
			fmt.Fprintln(os.Stderr, "[frontend]", err)
		}
	}()
	if frontend != nil {
		fmt.Printf("Athena is ready: http://%s\n", frontend.address)
	} else {
		fmt.Printf("Athena API is ready: http://127.0.0.1:%d\n", defaultClientHTTPPort)
	}
	return supervisor.Run(ctx, control, func() ([]packageUpdate, error) {
		remote, err := loadManifest(ctx, opts.manifestSource)
		if err != nil {
			return nil, err
		}
		return checkPackageUpdates(opts.home, remote), nil
	})
}

func startDetached(opts options) error {
	if startupCenterHealthy() {
		requestStartupUpdateCheck()
		fmt.Printf("Athena is already running; checking updates at http://127.0.0.1:%d\n", defaultStartupPort)
		return nil
	}
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
	fmt.Printf("Athena UI: %s\n", statusLabel(healthyURL(fmt.Sprintf("http://127.0.0.1:%d/", defaultFrontendPort))))
	fmt.Printf("Startup Center: %s\n", statusLabel(startupCenterHealthy()))
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

func startupCenterHealthy() bool {
	client := &http.Client{Timeout: time.Second}
	address := fmt.Sprintf("http://127.0.0.1:%d/healthz", defaultStartupPort)
	resp, err := client.Get(address)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

func requestStartupUpdateCheck() {
	client := &http.Client{Timeout: 2 * time.Second}
	address := fmt.Sprintf("http://127.0.0.1:%d/api/update/check", defaultStartupPort)
	request, err := http.NewRequest(http.MethodPost, address, nil)
	if err != nil {
		return
	}
	resp, err := client.Do(request)
	if err == nil {
		_ = resp.Body.Close()
	}
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
  athena-launcher launch  [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher start   [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher run     [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher install [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher update  [--home PATH] [--manifest URL_OR_FILE]
  athena-launcher validate [--manifest URL_OR_FILE]
  athena-launcher stop    [--home PATH]
  athena-launcher status  [--home PATH]
  athena-launcher version

Environment:
  ATHENA_HOME          installation and data directory (default ~/.athena)
  ATHENA_MANIFEST_URL release manifest HTTPS URL or local file`)
}
