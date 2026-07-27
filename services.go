package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	home        string
	paths       *generatedPaths
	manifest    *Manifest
	executables map[string]string
	processes   map[string]*managedProcess
	exits       chan processExit
	stopping    bool
}

func newSupervisor(home string, paths *generatedPaths, manifest *Manifest, executables map[string]string) *supervisor {
	return &supervisor{home: home, paths: paths, manifest: manifest, executables: executables, processes: make(map[string]*managedProcess), exits: make(chan processExit, len(manifest.Services)*2)}
}

func (s *supervisor) StartAll(ctx context.Context) error {
	for _, spec := range s.manifest.Services {
		if err := s.start(ctx, spec); err != nil {
			s.StopAll()
			return err
		}
	}
	return nil
}

func (s *supervisor) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			s.stopping = true
			s.StopAll()
			return nil
		case event := <-s.exits:
			if s.stopping {
				continue
			}
			fmt.Printf("[%s] exited: %v; restarting in 2 seconds\n", event.name, event.err)
			time.Sleep(2 * time.Second)
			spec, ok := s.service(event.name)
			if !ok {
				return fmt.Errorf("unknown exited service %s", event.name)
			}
			if err := s.start(ctx, spec); err != nil {
				return fmt.Errorf("restart %s: %w", event.name, err)
			}
		}
	}
}

func (s *supervisor) start(ctx context.Context, spec ServiceSpec) error {
	executable := s.executables[spec.Name]
	if executable == "" {
		return fmt.Errorf("service %s is not installed", spec.Name)
	}
	values := map[string]string{"{home}": s.home, "{config}": filepath.Dir(s.paths.clientConfig), "{install}": filepath.Dir(executable)}
	args := expandValues(spec.Args, values)
	env := append([]string{}, os.Environ()...)
	for key, value := range spec.Env {
		env = append(env, key+"="+expandValue(value, values))
	}
	switch spec.Name {
	case "agent-runtime":
		env = append(env, "AGENT_RUNTIME_CONFIG="+s.paths.runtimeConfig)
	case "agent-runtime-client":
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
	}
	return nil
}

func (s *supervisor) StopAll() {
	for i := len(s.manifest.Services) - 1; i >= 0; i-- {
		name := s.manifest.Services[i].Name
		process := s.processes[name]
		if process == nil || process.cmd == nil || process.cmd.Process == nil {
			continue
		}
		_ = process.cmd.Process.Signal(os.Interrupt)
		select {
		case <-process.done:
		case <-time.After(10 * time.Second):
			_ = process.cmd.Process.Kill()
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
