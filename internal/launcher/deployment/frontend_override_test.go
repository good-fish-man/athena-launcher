package deployment

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeFrontendOverrideRequiresBuiltIndex(t *testing.T) {
	dir := t.TempDir()
	if _, err := normalizeFrontendOverride(dir); err == nil {
		t.Fatal("frontend override without index.html must fail")
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("Athena"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := normalizeFrontendOverride(dir)
	if err != nil {
		t.Fatalf("normalize frontend override: %v", err)
	}
	if resolved != dir {
		t.Fatalf("resolved frontend = %q, want %q", resolved, dir)
	}
}

func TestLocalFrontendOverrideFiltersFrontendUpdates(t *testing.T) {
	updates := []packageUpdate{{Component: "agent-runtime"}, {Component: "frontend"}}
	filtered := withoutPackageComponent(updates, "frontend")
	if len(filtered) != 1 || filtered[0].Component != "agent-runtime" {
		t.Fatalf("filtered updates = %+v", filtered)
	}
}
