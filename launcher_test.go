package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestValidation(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	manifest := &Manifest{
		Version: "1.0.0",
		Database: DatabaseSpec{
			Version:   "16.3",
			BinDir:    "bin",
			Artifacts: map[string]Artifact{platformKey(): {URL: "https://downloads.example/postgres.tar.gz", SHA256: checksum, Format: "tar.gz"}},
		},
		Services: []ServiceSpec{{
			Name: "agent-runtime", Order: 1,
			Artifacts: map[string]Artifact{platformKey(): {URL: "https://downloads.example/runtime.tar.gz", SHA256: checksum, Format: "tar.gz", Executable: "agent-runtime"}},
		}},
	}
	if err := manifest.validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	manifest.Services[0].Artifacts[platformKey()] = Artifact{URL: "https://downloads.example/runtime", SHA256: "bad", Executable: "../runtime"}
	if err := manifest.validate(); err == nil {
		t.Fatal("unsafe manifest was accepted")
	}
}

func TestSafeArchivePath(t *testing.T) {
	root := t.TempDir()
	if _, err := safeArchivePath(root, "bin/agent-runtime"); err != nil {
		t.Fatalf("safe path rejected: %v", err)
	}
	for _, value := range []string{"../escape", "bin/../../escape", "/absolute/path"} {
		if _, err := safeArchivePath(root, value); err == nil {
			t.Fatalf("unsafe path %q was accepted", value)
		}
	}
}

func TestInstallRawArtifactAndChecksum(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "source")
	content := []byte("fake service binary")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	target := filepath.Join(home, "services", "test", "1.0.0")
	artifact := Artifact{URL: source, SHA256: hex.EncodeToString(sum[:]), Format: "raw", Executable: "test-service"}
	if err := installArtifact(context.Background(), home, target, artifact); err != nil {
		t.Fatalf("install artifact: %v", err)
	}
	installed, err := os.ReadFile(filepath.Join(target, "test-service"))
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(content) {
		t.Fatalf("installed content = %q", installed)
	}
	artifact.SHA256 = strings.Repeat("0", 64)
	if err := installArtifact(context.Background(), home, target, artifact); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
}

func TestGeneratedConfigsUseManagedDatabase(t *testing.T) {
	home := t.TempDir()
	state := &launcherState{DBPassword: "test-secret"}
	executables := map[string]string{"agent-runtime": filepath.Join(home, "services", "agent-runtime", "1", "agent-runtime")}
	paths, err := writeGeneratedConfigs(home, state, executables)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.clientConfig, paths.runtimeConfig} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "test-secret") || !strings.Contains(text, "db_port: 15432") {
			t.Fatalf("managed database settings missing from %s", path)
		}
	}
	skills, err := os.ReadFile(paths.skillsConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(skills) != "skills: {}\n" {
		t.Fatalf("unexpected skills config: %q", skills)
	}
}

func TestPlainHTTPOnlyAllowedForLoopback(t *testing.T) {
	remote, _ := url.Parse("http://downloads.example/file")
	if err := validateDownloadURL(remote); err == nil {
		t.Fatal("plain HTTP remote URL was accepted")
	}
	local, _ := url.Parse("http://127.0.0.1:8080/file")
	if err := validateDownloadURL(local); err != nil {
		t.Fatalf("loopback URL rejected: %v", err)
	}
}
