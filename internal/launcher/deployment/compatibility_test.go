package deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ga "github.com/good-fish-man/athena-protocol/protocol/ga/v1"
)

func TestVerifyReleaseCompatibilityMatchesEveryComponent(t *testing.T) {
	matrix := validCompatibilityMatrix()
	data, err := json.Marshal(matrix)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	manifest := compatibilityManifest("https://releases.example/compatibility-v1.0.json", hex.EncodeToString(digest[:]))
	if err := verifyReleaseCompatibilityData(data, manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Services[0].Version = "0.9.0"
	if err := verifyReleaseCompatibilityData(data, manifest); err == nil {
		t.Fatal("expected component version mismatch to fail")
	}
}

func TestStrictJSONRejectsUnknownCompatibilityFields(t *testing.T) {
	data := []byte(`{"schema":"athena.ga.v1","unknown":true}`)
	var matrix ga.CompatibilityMatrix
	if err := decodeStrictJSON(data, &matrix); err == nil {
		t.Fatal("expected unknown compatibility field to fail")
	}
}

func TestMaterializeCompatibilityMatrixForPatchRelease(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed")
	}
	baseline, err := json.Marshal(validCompatibilityMatrix("1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	input := filepath.Join(temporary, "baseline.json")
	output := filepath.Join(temporary, "compatibility.json")
	if err := os.WriteFile(input, baseline, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "../../../scripts/materialize-compatibility-matrix.sh", input, output, "v1.1.1", "0.9.0")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("materialize compatibility matrix: %v: %s", err, data)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var matrix ga.CompatibilityMatrix
	if err := json.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.ReleaseVersion != "1.1.1" || matrix.ProtocolVersion != "1.0.0" {
		t.Fatalf("materialized versions = release %s protocol %s", matrix.ReleaseVersion, matrix.ProtocolVersion)
	}
}

func validCompatibilityMatrix(releaseVersion ...string) ga.CompatibilityMatrix {
	release := "1.1.1"
	if len(releaseVersion) > 0 {
		release = releaseVersion[0]
	}
	matrix := ga.CompatibilityMatrix{
		Schema: ga.Schema, ReleaseVersion: release, ProtocolVersion: ga.ProtocolVersion,
		MinimumUpgradeVersion: "0.9.0", GeneratedAt: time.Now().UTC(),
		StableContracts: []ga.ContractPin{{
			Name: "agent", Schema: "athena.agent.v4", Version: "1.0.0",
			Path: "schema/v4/action.schema.json", SHA256: strings.Repeat("a", 64), Stability: "FROZEN",
		}},
	}
	for _, component := range ga.RequiredComponents {
		version := release
		if component == "athena-protocol" {
			version = ga.ProtocolVersion
		}
		matrix.Components = append(matrix.Components, ga.ComponentCompatibility{
			Component: component, Version: version, MinimumPeerVersion: "0.9.0",
			MaximumPeerVersion: "1.1.999", RequiredContracts: []string{"athena.agent.v4"},
		})
	}
	return matrix
}

func compatibilityManifest(url, digest string) *Manifest {
	return &Manifest{
		Version: "1.1.1", ProtocolVersion: "1.0.0", MinimumFromVersion: "0.9.0",
		CompatibilityURL: url, CompatibilitySHA256: digest,
		Services: []ServiceSpec{
			{Name: "agent-runtime", Version: "1.1.1"},
			{Name: "agent-runtime-client", Version: "1.1.1"},
		},
		Frontend: &FrontendSpec{Version: "1.1.1"},
	}
}
