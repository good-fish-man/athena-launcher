package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestSignedManifestRejectsTampering(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(time.Now().UTC())
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate("darwin-arm64"); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := manifest.Verify(publicKey, time.Now().UTC()); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	manifest.Services[0].Artifacts["darwin-arm64"] = Artifact{
		URL: "https://attacker.example/runtime", SHA256: strings.Repeat("b", 64), SBOMSHA256: strings.Repeat("a", 64),
		SizeBytes: 2048, CodeSigning: "NOTARIZED", Signature: manifest.Services[0].Artifacts["darwin-arm64"].Signature,
	}
	if err := manifest.Verify(publicKey, time.Now().UTC()); err == nil {
		t.Fatal("Verify() accepted a tampered artifact")
	}
}

func TestSignedManifestRejectsExpiredAndWrongKey(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	otherPublicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	manifest := testManifest(time.Now().UTC().Add(-48 * time.Hour))
	manifest.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Verify(publicKey, time.Now().UTC()); err == nil {
		t.Fatal("Verify() accepted an expired manifest")
	}
	manifest = testManifest(time.Now().UTC())
	if err := manifest.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Verify(otherPublicKey, time.Now().UTC()); err == nil {
		t.Fatal("Verify() accepted the wrong release identity")
	}
}

func TestManifestUpgradeFloor(t *testing.T) {
	manifest := testManifest(time.Now().UTC())
	manifest.MinimumFromVersion = "0.8.0"
	if manifest.AllowsUpgradeFrom("0.7.9") {
		t.Fatal("upgrade below the supported floor was accepted")
	}
	if !manifest.AllowsUpgradeFrom("v0.8.0") || !manifest.AllowsUpgradeFrom("1.0.0") {
		t.Fatal("supported upgrade was rejected")
	}
}

func TestGAManifestPinsCompatibilityAndComponentVersions(t *testing.T) {
	manifest := testManifest(time.Now().UTC())
	manifest.Version = "1.0.0"
	manifest.ReleaseID = "athena-v1.0.0"
	manifest.ProtocolVersion = ProtocolVersion
	manifest.MinimumFromVersion = "0.9.0"
	manifest.CompatibilityURL = "https://releases.example/compatibility-v1.0.json"
	manifest.CompatibilitySHA256 = strings.Repeat("b", 64)
	manifest.Services = append(manifest.Services,
		ServiceSpec{Name: "agent-runtime-client", Version: "1.0.0", Order: 20, HealthURL: "http://127.0.0.1:8090/healthz", Artifacts: manifest.Services[0].Artifacts},
	)
	manifest.Services[0].Version = "1.0.0"
	manifest.Frontend = &FrontendSpec{Version: "1.0.0", Artifacts: manifest.Services[0].Artifacts}
	if err := manifest.ValidateGA(); err != nil {
		t.Fatal(err)
	}
	manifest.Services[0].Version = ""
	if err := manifest.ValidateGA(); err == nil {
		t.Fatal("GA manifest accepted an unpinned service version")
	}
}

func TestDevelopmentManifestAllowsAbsoluteLocalArtifact(t *testing.T) {
	manifest := testManifest(time.Now().UTC())
	manifest.Development = true
	artifact := manifest.Database.Artifacts["darwin-arm64"]
	artifact.URL = "file:///private/tmp/athena%20artifact.tar.gz"
	artifact.CodeSigning = "DEVELOPMENT"
	manifest.Database.Artifacts["darwin-arm64"] = artifact
	manifest.Services[0].Artifacts["darwin-arm64"] = artifact

	if err := manifest.Validate("darwin-arm64"); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestProductionManifestRejectsLocalArtifact(t *testing.T) {
	manifest := testManifest(time.Now().UTC())
	artifact := manifest.Database.Artifacts["darwin-arm64"]
	artifact.URL = "file:///private/tmp/runtime.tar.gz"
	manifest.Database.Artifacts["darwin-arm64"] = artifact

	if err := manifest.Validate("darwin-arm64"); err == nil {
		t.Fatal("Validate() accepted a local production artifact")
	}
}

func TestDevelopmentManifestRejectsUnsafeLocalArtifact(t *testing.T) {
	for _, artifactURL := range []string{
		"file://remote-host/private/tmp/runtime.tar.gz",
		"file:///private/tmp/../runtime.tar.gz",
		"file:///private/tmp/runtime.tar.gz?version=1",
		"runtime.tar.gz",
	} {
		manifest := testManifest(time.Now().UTC())
		manifest.Development = true
		artifact := manifest.Database.Artifacts["darwin-arm64"]
		artifact.URL = artifactURL
		artifact.CodeSigning = "DEVELOPMENT"
		manifest.Database.Artifacts["darwin-arm64"] = artifact
		if err := manifest.Validate("darwin-arm64"); err == nil {
			t.Fatalf("Validate() accepted unsafe development artifact %q", artifactURL)
		}
	}
}

func testManifest(now time.Time) *Manifest {
	hash := strings.Repeat("a", 64)
	artifact := Artifact{URL: "https://releases.example/runtime.tar.gz", SHA256: hash, SizeBytes: 1024, SBOMSHA256: hash, CodeSigning: "NOTARIZED", Format: "tar.gz", Executable: "agent-runtime"}
	return &Manifest{
		Schema: ManifestSchema, ReleaseID: "athena-v0.9.0", Version: "0.9.0", ProtocolVersion: ProtocolVersion,
		MinimumFromVersion: "0.8.0", SBOMURL: "https://releases.example/release-sbom.spdx.json", SBOMSHA256: hash,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(30 * 24 * time.Hour),
		Database: DatabaseSpec{Version: "16.13.0", BinDir: "bin", Artifacts: map[string]Artifact{"darwin-arm64": artifact}},
		Services: []ServiceSpec{{Name: "agent-runtime", Order: 10, HealthURL: "http://127.0.0.1:18081/healthz", Artifacts: map[string]Artifact{"darwin-arm64": artifact}}},
	}
}
