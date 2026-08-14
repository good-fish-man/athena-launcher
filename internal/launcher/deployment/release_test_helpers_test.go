package deployment

import (
	"strings"
	"time"

	releasepkg "athena-launcher/internal/release"
)

func completeDevelopmentManifest(manifest *Manifest) *Manifest {
	if manifest == nil {
		return nil
	}
	now := time.Now().UTC()
	manifest.Schema = releasepkg.ManifestSchema
	manifest.ReleaseID = "athena-test-" + manifest.Version
	manifest.ProtocolVersion = releasepkg.ProtocolVersion
	manifest.MinimumFromVersion = "0.0.0"
	manifest.Development = true
	manifest.SBOMURL = "https://releases.example/release-sbom.spdx.json"
	manifest.SBOMSHA256 = strings.Repeat("d", 64)
	if compareReleaseSemver(manifest.Version, "1.0.0") >= 0 {
		manifest.CompatibilityURL = "https://releases.example/compatibility-v1.0.json"
		manifest.CompatibilitySHA256 = strings.Repeat("c", 64)
	}
	manifest.IssuedAt = now.Add(-time.Minute)
	manifest.ExpiresAt = now.Add(24 * time.Hour)
	complete := func(artifact Artifact) Artifact {
		if artifact.SBOMSHA256 == "" {
			artifact.SBOMSHA256 = manifest.SBOMSHA256
		}
		if artifact.CodeSigning == "" {
			artifact.CodeSigning = "DEVELOPMENT"
		}
		if artifact.SizeBytes <= 0 {
			artifact.SizeBytes = 1
		}
		return artifact
	}
	for platform, artifact := range manifest.Database.Artifacts {
		manifest.Database.Artifacts[platform] = complete(artifact)
	}
	if manifest.Browser != nil {
		for platform, artifact := range manifest.Browser.Artifacts {
			manifest.Browser.Artifacts[platform] = complete(artifact)
		}
	}
	for index := range manifest.Services {
		if manifest.Services[index].Version == "" && compareReleaseSemver(manifest.Version, "1.0.0") >= 0 {
			manifest.Services[index].Version = manifest.Version
		}
		for platform, artifact := range manifest.Services[index].Artifacts {
			manifest.Services[index].Artifacts[platform] = complete(artifact)
		}
	}
	if manifest.Frontend != nil {
		if manifest.Frontend.Version == "" && compareReleaseSemver(manifest.Version, "1.0.0") >= 0 {
			manifest.Frontend.Version = manifest.Version
		}
		for platform, artifact := range manifest.Frontend.Artifacts {
			manifest.Frontend.Artifacts[platform] = complete(artifact)
		}
	}
	return manifest
}
