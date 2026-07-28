package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupervisorReturnsUpdateRequestAfterApproval(t *testing.T) {
	tracker := newStartupTracker(t.TempDir())
	tracker.ready("http://127.0.0.1:3000/")
	tracker.offerUpdate([]packageUpdate{{Component: "agent-runtime", DisplayName: "Agent Runtime", CurrentHash: "old", RemoteHash: "new"}}, true)
	control := newStartupController()
	control.applyUpdate <- struct{}{}
	supervisor := &supervisor{tracker: tracker, exits: make(chan processExit, 1)}

	err := supervisor.Run(context.Background(), control, func() ([]packageUpdate, error) { return nil, nil })
	if !errors.Is(err, errUpdateRequested) {
		t.Fatalf("supervisor.Run() error = %v, want errUpdateRequested", err)
	}
}

func TestCheckPackageUpdatesComparesInstalledMarkers(t *testing.T) {
	home := t.TempDir()
	manifest := updateTestManifest()
	writeArtifactMarker(t, filepath.Join(home, "postgres", "16.12.0"), databaseArtifactMarker("old-db"))
	writeArtifactMarker(t, filepath.Join(home, "services", "agent-runtime", "0.1.0"), "old-runtime")
	writeArtifactMarker(t, filepath.Join(home, "services", "agent-runtime-client", "0.1.0"), "client-hash")
	writeArtifactMarker(t, filepath.Join(home, "frontend", "0.1.0"), "ui-hash")

	updates := checkPackageUpdates(home, manifest)
	if len(updates) != 2 {
		t.Fatalf("checkPackageUpdates() returned %d updates: %+v", len(updates), updates)
	}
	if updates[0].Component != "postgres" || updates[1].Component != "agent-runtime" {
		t.Fatalf("unexpected updates: %+v", updates)
	}
	if updates[1].CurrentHash != "old-runtime" || updates[1].RemoteHash != "runtime-hash" {
		t.Fatalf("unexpected runtime hashes: %+v", updates[1])
	}
}

func TestCheckPackageUpdatesIgnoresMissingAndMatchingPackages(t *testing.T) {
	home := t.TempDir()
	manifest := updateTestManifest()
	writeArtifactMarker(t, filepath.Join(home, "services", "agent-runtime", "0.1.0"), "runtime-hash")

	if updates := checkPackageUpdates(home, manifest); len(updates) != 0 {
		t.Fatalf("matching or missing packages reported as updates: %+v", updates)
	}
}

func TestFindVerifiedArtifactForCrossVersionReuse(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "0.1.0")
	writeArtifactMarker(t, installed, "same-hash")
	executable := filepath.Join(installed, "package", "agent-runtime")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}

	if got := findArtifactRootByMarker(root, "SAME-HASH"); got != installed {
		t.Fatalf("findArtifactRootByMarker() = %q, want %q", got, installed)
	}
	if got, err := findInstalledExecutable(installed, "agent-runtime"); err != nil || got != executable {
		t.Fatalf("findInstalledExecutable() = %q, %v", got, err)
	}
}

func TestSaveInstalledManifest(t *testing.T) {
	home := t.TempDir()
	manifest := updateTestManifest()
	if err := saveInstalledManifest(home, manifest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, installedManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": "0.2.0"`) {
		t.Fatalf("unexpected installed manifest: %s", data)
	}
}

func updateTestManifest() *Manifest {
	platform := platformKey()
	return &Manifest{
		Version: "0.2.0",
		Database: DatabaseSpec{Version: "16.13.0", Artifacts: map[string]Artifact{
			platform: {SHA256: "new-db"},
		}},
		Services: []ServiceSpec{
			{Name: "agent-runtime", Artifacts: map[string]Artifact{platform: {SHA256: "runtime-hash"}}},
			{Name: "agent-runtime-client", Artifacts: map[string]Artifact{platform: {SHA256: "client-hash"}}},
		},
		Frontend: &FrontendSpec{Artifacts: map[string]Artifact{platform: {SHA256: "ui-hash"}}},
	}
}

func writeArtifactMarker(t *testing.T, root, hash string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".artifact-sha256"), []byte(hash+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
