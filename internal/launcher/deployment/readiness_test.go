package deployment

import (
	"strings"
	"testing"

	ga "github.com/good-fish-man/athena-protocol/protocol/ga/v1"
)

func TestLauncherAggregateStatusDoesNotHideExternalGatesOrFailures(t *testing.T) {
	checks := []ga.ReadinessCheck{
		launcherCheck("local", "test", ga.StatusPass, "ok"),
		launcherCheck("notarization", "test", ga.StatusExternalRequired, "required"),
	}
	if status := launcherAggregateStatus(checks); status != ga.StatusExternalRequired {
		t.Fatalf("status = %s", status)
	}
	checks = append(checks, launcherCheck("identity", "test", ga.StatusFail, "failed"))
	if status := launcherAggregateStatus(checks); status != ga.StatusFail {
		t.Fatalf("status = %s", status)
	}
}

func TestPlatformSigningReadinessFailsOpenClaims(t *testing.T) {
	manifest := updateTestManifest()
	manifest.Version = "1.0.0"
	manifest.ProtocolVersion = "1.0.0"
	manifest.MinimumFromVersion = "0.9.0"
	manifest.CompatibilityURL = "https://example.test/compatibility.json"
	manifest.CompatibilitySHA256 = strings.Repeat("a", 64)
	manifest.Frontend.Version = "1.0.0"
	for index := range manifest.Services {
		manifest.Services[index].Version = "1.0.0"
	}
	status, _ := platformSigningReadiness(manifest)
	if status != ga.StatusExternalRequired {
		t.Fatalf("development signing status = %s", status)
	}
}
