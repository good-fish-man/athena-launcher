package deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func validCompatibilityMatrix() ga.CompatibilityMatrix {
	matrix := ga.CompatibilityMatrix{
		Schema: ga.Schema, ReleaseVersion: ga.ReleaseVersion, ProtocolVersion: ga.ProtocolVersion,
		MinimumUpgradeVersion: "0.9.0", GeneratedAt: time.Now().UTC(),
		StableContracts: []ga.ContractPin{{
			Name: "agent", Schema: "athena.agent.v4", Version: "1.0.0",
			Path: "schema/v4/action.schema.json", SHA256: strings.Repeat("a", 64), Stability: "FROZEN",
		}},
	}
	for _, component := range ga.RequiredComponents {
		matrix.Components = append(matrix.Components, ga.ComponentCompatibility{
			Component: component, Version: "1.0.0", MinimumPeerVersion: "0.9.0",
			MaximumPeerVersion: "1.0.999", RequiredContracts: []string{"athena.agent.v4"},
		})
	}
	return matrix
}

func compatibilityManifest(url, digest string) *Manifest {
	return &Manifest{
		Version: "1.0.0", ProtocolVersion: "1.0.0", MinimumFromVersion: "0.9.0",
		CompatibilityURL: url, CompatibilitySHA256: digest,
		Services: []ServiceSpec{
			{Name: "agent-runtime", Version: "1.0.0"},
			{Name: "agent-runtime-client", Version: "1.0.0"},
		},
		Frontend: &FrontendSpec{Version: "1.0.0"},
	}
}
