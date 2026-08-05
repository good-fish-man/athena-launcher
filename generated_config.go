package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type generatedPaths struct {
	clientConfig  string
	runtimeConfig string
	skillsConfig  string
}

func writeGeneratedConfigs(home string, state *launcherState, executables map[string]string) (*generatedPaths, error) {
	configDir := filepath.Join(home, "config")
	dataDir := filepath.Join(home, "data")
	uploadsDir := filepath.Join(dataDir, "uploads")
	userSkillsDir := filepath.Join(dataDir, "skills")
	for _, directory := range []string{configDir, uploadsDir, userSkillsDir, filepath.Join(home, "logs")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	paths := &generatedPaths{
		clientConfig: filepath.Join(configDir, "agent-runtime-client.yaml"), runtimeConfig: filepath.Join(configDir, "agent-runtime.yaml"),
		skillsConfig: filepath.Join(configDir, "skills-config.yaml"),
	}
	runtimeSkillsDir := ""
	if executable := executables["agent-runtime"]; executable != "" {
		runtimeSkillsDir = filepath.Join(filepath.Dir(executable), "skills")
	}
	clientYAML := fmt.Sprintf(`server:
  name: "agent-runtime-client"
  http_addr: ":%d"
  mode: "release"
  public_prefix: "/api/agent-runtime-client/v1"

runtime:
  grpc_addr: "127.0.0.1:%d"
  http_addr: "http://127.0.0.1:%d"
  request_timeout_sec: 120
  dial_timeout_sec: 10

control:
  device_token: %s

scheduled_task:
  scan_interval_sec: 60

log:
  level: "info"

db:
  db_type: "postgres"
  username: %s
  password: %s
  db_host: "127.0.0.1"
  db_port: %d
  db_name: %s
  charset: "utf8mb4"
  max_open_conn: 50
  max_idle_conn: 10
  conn_max_lifetime: 500
  log_mode: 3
  slow_threshold: 200

paths:
  app_config_file: %s
  skills_config_file: %s
  uploads_dir: %s
`, defaultClientHTTPPort, defaultRuntimeGRPCPort, defaultRuntimeHTTPPort, yamlString(state.InternalServiceToken),
		yamlString(defaultDatabaseUser), yamlString(state.DBPassword), defaultDatabasePort, yamlString(defaultDatabaseName),
		yamlString(paths.clientConfig), yamlString(paths.skillsConfig), yamlString(uploadsDir))
	runtimeYAML := fmt.Sprintf(`server:
  grpc_addr: ":%d"
  http_addr: ":%d"
  default_model:
    provider: ""
    name: ""
    api_key: ""
    api_base: ""

db:
  enabled: true
  db_type: "postgres"
  username: %s
  password: %s
  db_host: "127.0.0.1"
  db_port: %d
  db_name: %s
  ssl_mode: "disable"
  max_open_conn: 50
  max_idle_conn: 10
  conn_max_lifetime: 500
  log_mode: 3

memory:
  enabled: true
  auto_migrate: true
  inject_into_prompt: true
  background_review: true
  max_review_memory: 10

sandbox:
  default_image: "alpine:latest"
  pptx_image: "node:18-alpine"
  workdir: "/workspace"
  timeout_ms: 120000

skills:
  dir: %s
  config_path: %s
  global_dir: %s
`, defaultRuntimeGRPCPort, defaultRuntimeHTTPPort,
		yamlString(defaultDatabaseUser), yamlString(state.DBPassword), defaultDatabasePort, yamlString(defaultDatabaseName),
		yamlString(userSkillsDir), yamlString(paths.skillsConfig), yamlString(runtimeSkillsDir))
	if err := writeAtomic(paths.clientConfig, []byte(clientYAML), 0o600); err != nil {
		return nil, err
	}
	if err := writeAtomic(paths.runtimeConfig, []byte(runtimeYAML), 0o600); err != nil {
		return nil, err
	}
	if _, err := os.Stat(paths.skillsConfig); os.IsNotExist(err) {
		if err := writeAtomic(paths.skillsConfig, []byte("skills: {}\n"), 0o600); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func yamlString(value string) string { return strconv.Quote(value) }

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
