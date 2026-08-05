package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type managedServicePort struct {
	Service string
	Port    int
	Reason  string
}

type portOwner struct {
	PID     int
	Command string
	Args    string
}

func managedServicePorts(manifest *Manifest) []managedServicePort {
	seen := make(map[string]struct{})
	var ports []managedServicePort
	add := func(service string, port int, reason string) {
		key := service + ":" + strconv.Itoa(port)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		ports = append(ports, managedServicePort{Service: service, Port: port, Reason: reason})
	}
	for _, service := range manifest.Services {
		switch service.Name {
		case "agent-runtime":
			add(service.Name, defaultRuntimeGRPCPort, "gRPC")
			add(service.Name, defaultRuntimeHTTPPort, "health")
		case "agent-runtime-client":
			add(service.Name, defaultClientHTTPPort, "HTTP/API")
		}
	}
	return ports
}

func prepareManagedServicePorts(ctx context.Context, home string, manifest *Manifest) error {
	for _, managedPort := range managedServicePorts(manifest) {
		if portAvailable(uint32(managedPort.Port)) {
			continue
		}
		owner, found, err := findPortOwner(ctx, managedPort.Port)
		if err != nil {
			return fmt.Errorf("%s %s port %d is already in use and owner lookup failed: %w", managedPort.Service, managedPort.Reason, managedPort.Port, err)
		}
		if !found {
			return fmt.Errorf("%s %s port %d is already in use", managedPort.Service, managedPort.Reason, managedPort.Port)
		}
		if !isAthenaManagedProcess(home, managedPort.Service, owner) {
			return fmt.Errorf("%s %s port %d is already in use by pid %d (%s); stop that program or change Athena ports before launching", managedPort.Service, managedPort.Reason, managedPort.Port, owner.PID, ownerLabel(owner))
		}
		fmt.Printf("[%s] stopping previous Athena process on port %d pid=%d (%s)\n", managedPort.Service, managedPort.Port, owner.PID, ownerLabel(owner))
		if err := terminatePortOwner(ctx, owner); err != nil {
			return fmt.Errorf("stop previous %s on port %d pid %d: %w", managedPort.Service, managedPort.Port, owner.PID, err)
		}
		if err := waitPortAvailable(ctx, managedPort.Port, 10*time.Second); err != nil {
			return fmt.Errorf("%s %s port %d did not become available after stopping pid %d: %w", managedPort.Service, managedPort.Reason, managedPort.Port, owner.PID, err)
		}
	}
	return nil
}

func isAthenaManagedProcess(home, service string, owner portOwner) bool {
	text := strings.ToLower(owner.Command + " " + owner.Args)
	service = strings.ToLower(strings.TrimSpace(service))
	if service != "" && strings.Contains(text, service) {
		return true
	}
	if strings.Contains(text, "___go_bui") || strings.Contains(text, "__debug_bin") {
		return true
	}
	if home != "" {
		if absoluteHome, err := filepath.Abs(home); err == nil {
			home = absoluteHome
		}
		for _, marker := range []string{
			filepath.Join(home, "services"),
			filepath.Join(home, "browser"),
		} {
			if strings.Contains(text, strings.ToLower(marker)) || strings.Contains(text, strings.ToLower(filepath.ToSlash(marker))) {
				return true
			}
		}
	}
	return false
}

func terminatePortOwner(ctx context.Context, owner portOwner) error {
	process, err := os.FindProcess(owner.PID)
	if err != nil {
		return err
	}
	for _, signal := range terminationSignals() {
		if err := process.Signal(signal); err == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1200 * time.Millisecond):
			}
			if !processStillExists(owner.PID) {
				return nil
			}
		}
	}
	if err := process.Kill(); err != nil && processStillExists(owner.PID) {
		return err
	}
	return nil
}

func waitPortAvailable(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if portAvailable(uint32(port)) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out after %s", timeout)
		case <-ticker.C:
		}
	}
}

func ownerLabel(owner portOwner) string {
	if strings.TrimSpace(owner.Args) != "" {
		return strings.TrimSpace(owner.Args)
	}
	if strings.TrimSpace(owner.Command) != "" {
		return strings.TrimSpace(owner.Command)
	}
	return "unknown"
}
