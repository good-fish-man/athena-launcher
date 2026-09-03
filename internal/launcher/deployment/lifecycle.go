package deployment

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

func startDetached(opts options) error {
	if startupCenterHealthy() {
		requestStartupUpdateCheck()
		fmt.Printf("Athena is already running; checking updates at http://127.0.0.1:%d\n", defaultStartupPort)
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(opts.home, "logs"), 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(opts.home, "logs", "log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	commandArgs := []string{"run", "--home", opts.home, "--manifest", opts.manifestSource}
	if opts.frontendDir != "" {
		commandArgs = append(commandArgs, "--frontend-dir", opts.frontendDir)
	}
	command := exec.Command(executable, commandArgs...)
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
	launcherPID := state.LauncherPID
	for attempt := 0; attempt < 40; attempt++ {
		current, err := loadState(home)
		if err == nil && (current == nil || current.LauncherPID == 0 || current.LauncherPID != launcherPID) {
			fmt.Println("Athena stopped.")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("launcher pid %d did not stop within 20 seconds; inspect %s", state.LauncherPID, filepath.Join(home, "logs", "log"))
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
	selection := deploymentFromState(state)
	if selection.Mode == connectionModeRemote {
		remoteHealthy := healthyURL(selection.RemoteURL + "/healthz")
		fmt.Println("Connection mode: remote")
		fmt.Printf("Remote Runtime Client: %s (%s)\n", statusLabel(remoteHealthy), selection.RemoteURL)
		fmt.Printf("Athena UI: %s (Wails desktop window)\n", statusLabel(pid > 0 && remoteHealthy))
		return
	}
	fmt.Println("Connection mode: local")
	runtimeHealthy := healthyURL(fmt.Sprintf("http://127.0.0.1:%d/healthz", defaultRuntimeHTTPPort))
	clientHealthy := healthyURL(fmt.Sprintf("http://127.0.0.1:%d/healthz", defaultClientHTTPPort))
	browserUI := healthyURL(fmt.Sprintf("http://127.0.0.1:%d/", defaultFrontendPort))
	desktopUI := pid > 0 && clientHealthy && !browserUI
	fmt.Printf("PostgreSQL: %s\n", statusLabel(tcpReachable(databasePort())))
	fmt.Printf("agent-runtime: %s\n", statusLabel(runtimeHealthy))
	fmt.Printf("agent-runtime-client: %s\n", statusLabel(clientHealthy))
	if desktopUI {
		fmt.Println("Athena UI: running (Wails desktop window)")
	} else {
		fmt.Printf("Athena UI: %s\n", statusLabel(browserUI))
	}
	if pid > 0 && !startupCenterHealthy() {
		fmt.Println("Startup Center: embedded in desktop window")
	} else {
		fmt.Printf("Startup Center: %s\n", statusLabel(startupCenterHealthy()))
	}
}

func watchStopRequest(ctx context.Context, cancel func(), path string) {
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
