package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	update := tracker.current().Update
	if update.State != "applying" || !strings.Contains(update.Message, "encrypted recovery point") {
		t.Fatalf("unexpected update protection state: %+v", update)
	}
}

func TestCheckPackageUpdatesComparesInstalledMarkers(t *testing.T) {
	home := t.TempDir()
	manifest := updateTestManifest()
	writeArtifactMarker(t, filepath.Join(home, "postgres", "16.12.0"), databaseArtifactMarker(testHash("old-db")))
	writeArtifactMarker(t, filepath.Join(home, "services", "agent-runtime", "0.1.0"), testHash("old-runtime"))
	writeArtifactMarker(t, filepath.Join(home, "services", "agent-runtime-client", "0.1.0"), testHash("client"))
	writeArtifactMarker(t, filepath.Join(home, "frontend", "0.1.0"), testHash("ui"))

	updates := checkPackageUpdates(home, manifest)
	if len(updates) != 2 {
		t.Fatalf("checkPackageUpdates() returned %d updates: %+v", len(updates), updates)
	}
	if updates[0].Component != "postgres" || updates[1].Component != "agent-runtime" {
		t.Fatalf("unexpected updates: %+v", updates)
	}
	if updates[1].CurrentHash != testHash("old-runtime") || updates[1].RemoteHash != testHash("runtime") {
		t.Fatalf("unexpected runtime hashes: %+v", updates[1])
	}
}

func TestCheckPackageUpdatesIgnoresMissingAndMatchingPackages(t *testing.T) {
	home := t.TempDir()
	manifest := updateTestManifest()
	writeArtifactMarker(t, filepath.Join(home, "services", "agent-runtime", "0.1.0"), testHash("runtime"))

	if updates := checkPackageUpdates(home, manifest); len(updates) != 0 {
		t.Fatalf("matching or missing packages reported as updates: %+v", updates)
	}
}

func TestCheckPackageUpdatesIncludesBrowserArtifact(t *testing.T) {
	home := t.TempDir()
	manifest := updateTestManifest()
	manifest.Browser = &BrowserSpec{Version: "0.33.1", Artifacts: map[string]Artifact{
		platformKey(): {URL: "https://downloads.example/agent-browser", SHA256: testHash("new-browser"), Executable: "agent-browser"},
	}}
	writeArtifactMarker(t, filepath.Join(home, "browser", "0.32.0"), testHash("old-browser"))
	updates := checkPackageUpdates(home, manifest)
	if len(updates) != 1 || updates[0].Component != "agent-browser" {
		t.Fatalf("browser update was not detected: %+v", updates)
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
	loaded, err := loadInstalledManifest(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != manifest.Version {
		t.Fatalf("loadInstalledManifest() version = %q, want %q", loaded.Version, manifest.Version)
	}
}

func TestPrepareUsesInstalledManifestWhileUpdateIsAvailable(t *testing.T) {
	home := t.TempDir()
	remote := updateTestManifest()
	installed := updateTestManifest()
	installed.Version = "0.1.0"
	installed.Database.Artifacts[platformKey()] = Artifact{URL: "https://downloads.example/old-db.tar.gz", SHA256: testHash("old-db")}
	installed.Services[0].Artifacts[platformKey()] = Artifact{URL: "https://downloads.example/old-runtime.tar.gz", SHA256: testHash("old-runtime"), Executable: "agent-runtime"}
	completeDevelopmentManifest(installed)
	if err := saveInstalledManifest(home, installed); err != nil {
		t.Fatal(err)
	}
	writeArtifactMarker(t, filepath.Join(home, "postgres", "16.13.0"), databaseArtifactMarker(testHash("old-db")))
	writeArtifactMarker(t, filepath.Join(home, "services", "agent-runtime", "0.1.0"), testHash("old-runtime"))
	writeArtifactMarker(t, filepath.Join(home, "services", "agent-runtime-client", "0.1.0"), testHash("client"))
	writeArtifactMarker(t, filepath.Join(home, "frontend", "0.1.0"), testHash("ui"))
	writeTestExecutable(t, filepath.Join(home, "services", "agent-runtime", "0.1.0", "agent-runtime"))
	writeTestExecutable(t, filepath.Join(home, "services", "agent-runtime-client", "0.1.0", "agent-runtime-client"))
	writeTestDatabaseBinaries(t, filepath.Join(home, "postgres", "16.13.0"))
	writeTestExecutable(t, filepath.Join(home, "frontend", "0.1.0", "index.html"))
	if err := validateInstalledPackages(home, installed); err != nil {
		t.Fatalf("installed release fixture is not usable: %v", err)
	}
	loadedInstalled, err := loadInstalledManifest(home)
	if err != nil {
		t.Fatalf("installed release fixture cannot be loaded: %v", err)
	}
	if err := validateInstalledPackages(home, loadedInstalled); err != nil {
		t.Fatalf("loaded installed release fixture is not usable: %v", err)
	}

	manifestPath := filepath.Join(home, "remote-manifest.json")
	data, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	control := newStartupController()
	tracker := newStartupTracker(home)
	selected, _, _, err := prepareWithTracker(context.Background(), options{home: home, manifestSource: manifestPath}, tracker, control, false)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Version != installed.Version {
		t.Fatalf("selected manifest version = %q, want installed %q", selected.Version, installed.Version)
	}
	if tracker.current().Update.State != "available" {
		t.Fatalf("update prompt was not preserved: %+v", tracker.current().Update)
	}
}

func TestInstalledPackagesUsableRejectsMissingService(t *testing.T) {
	home := t.TempDir()
	manifest := updateTestManifest()
	writeArtifactMarker(t, filepath.Join(home, "postgres", "16.13.0"), databaseArtifactMarker(testHash("new-db")))
	writeTestDatabaseBinaries(t, filepath.Join(home, "postgres", "16.13.0"))

	if installedPackagesUsable(home, manifest) {
		t.Fatal("installedPackagesUsable() accepted an installation with missing services")
	}
}

func updateTestManifest() *Manifest {
	platform := platformKey()
	return completeDevelopmentManifest(&Manifest{
		Version: "0.2.0",
		Database: DatabaseSpec{Version: "16.13.0", Artifacts: map[string]Artifact{
			platform: {URL: "https://downloads.example/db.tar.gz", SHA256: testHash("new-db")},
		}},
		Services: []ServiceSpec{
			{Name: "agent-runtime", Artifacts: map[string]Artifact{platform: {URL: "https://downloads.example/runtime.tar.gz", SHA256: testHash("runtime"), Executable: "agent-runtime"}}},
			{Name: "agent-runtime-client", Artifacts: map[string]Artifact{platform: {URL: "https://downloads.example/client.tar.gz", SHA256: testHash("client"), Executable: "agent-runtime-client"}}},
		},
		Frontend: &FrontendSpec{Artifacts: map[string]Artifact{platform: {URL: "https://downloads.example/ui.tar.gz", SHA256: testHash("ui")}}},
	})
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

func writeTestExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeTestDatabaseBinaries(t *testing.T, root string) {
	t.Helper()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, name := range []string{"initdb", "pg_ctl", "postgres"} {
		writeTestExecutable(t, filepath.Join(root, "bin", name+suffix))
	}
}

func testHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
