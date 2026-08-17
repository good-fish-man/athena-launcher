package deployment

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestAcceptsZIPArchive(t *testing.T) {
	archive := manifestZIP(t, manifestFileName)
	manifestPath := filepath.Join(t.TempDir(), "manifest-artifact.zip")
	if err := os.WriteFile(manifestPath, archive, 0o600); err != nil {
		t.Fatal(err)
	}

	manifest, err := loadManifest(t.Context(), manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != updateTestManifest().Version {
		t.Fatalf("manifest version = %q", manifest.Version)
	}
}

func TestLoadManifestFallsBackToZIPOnJSON404(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sbom := []byte(`{"SPDXID":"SPDXRef-DOCUMENT"}`)
	var archive []byte
	var requested []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requested = append(requested, request.URL.Path)
		switch path.Base(request.URL.Path) {
		case manifestFileName:
			http.NotFound(response, request)
		case manifestArchiveName:
			response.Header().Set("Content-Type", "application/zip")
			_, _ = response.Write(archive)
		case "release-sbom.spdx.json":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write(sbom)
		default:
			http.Error(response, "unexpected path", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	manifestFixture := productionManifestFixture(updateTestManifest())
	manifestFixture.SBOMURL = server.URL + "/release-sbom.spdx.json"
	sbomDigest := sha256.Sum256(sbom)
	manifestFixture.SBOMSHA256 = hex.EncodeToString(sbomDigest[:])
	if err := manifestFixture.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	archive = manifestZIPFor(t, manifestFixture, manifestFileName)
	t.Setenv("ATHENA_RELEASE_PUBLIC_KEY", base64.RawStdEncoding.EncodeToString(publicKey))
	originalClient := downloadClient
	downloadClient = server.Client()
	downloadClient.Timeout = originalClient.Timeout
	downloadClient.CheckRedirect = validateDownloadRedirect
	t.Cleanup(func() { downloadClient = originalClient })

	manifest, err := loadManifest(t.Context(), server.URL+"/releases/latest/download/"+manifestFileName)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != updateTestManifest().Version {
		t.Fatalf("manifest version = %q", manifest.Version)
	}
	want := []string{"/releases/latest/download/" + manifestFileName, "/releases/latest/download/" + manifestArchiveName, "/release-sbom.spdx.json"}
	if strings.Join(requested, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requested paths = %v, want %v", requested, want)
	}
}

func TestUnpackManifestRejectsDuplicateManifestFiles(t *testing.T) {
	archive := manifestZIP(t, manifestFileName, "nested/"+manifestFileName)
	if _, err := unpackManifest(archive, manifestSizeLimit); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("unpackManifest() error = %v", err)
	}
}

func TestReadLimitedRejectsOversizedContent(t *testing.T) {
	if _, err := readLimited(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("readLimited() accepted oversized content")
	}
}

func manifestZIP(t *testing.T, names ...string) []byte {
	return manifestZIPFor(t, updateTestManifest(), names...)
}

func manifestZIPFor(t *testing.T, manifest *Manifest, names ...string) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, name := range names {
		file, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func productionManifestFixture(manifest *Manifest) *Manifest {
	manifest.Development = false
	productionArtifact := func(artifact Artifact) Artifact {
		artifact.CodeSigning = "CHECKSUM_VERIFIED"
		return artifact
	}
	for platform, artifact := range manifest.Database.Artifacts {
		manifest.Database.Artifacts[platform] = productionArtifact(artifact)
	}
	if manifest.Browser != nil {
		for platform, artifact := range manifest.Browser.Artifacts {
			manifest.Browser.Artifacts[platform] = productionArtifact(artifact)
		}
	}
	for index := range manifest.Services {
		for platform, artifact := range manifest.Services[index].Artifacts {
			manifest.Services[index].Artifacts[platform] = productionArtifact(artifact)
		}
	}
	if manifest.Frontend != nil {
		for platform, artifact := range manifest.Frontend.Artifacts {
			manifest.Frontend.Artifacts[platform] = productionArtifact(artifact)
		}
	}
	return manifest
}
