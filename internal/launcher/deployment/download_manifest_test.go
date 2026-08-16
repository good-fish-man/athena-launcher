package deployment

import (
	"archive/zip"
	"bytes"
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
	archive := manifestZIP(t, manifestFileName)
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requested = append(requested, request.URL.Path)
		switch path.Base(request.URL.Path) {
		case manifestFileName:
			http.NotFound(response, request)
		case manifestArchiveName:
			response.Header().Set("Content-Type", "application/zip")
			_, _ = response.Write(archive)
		default:
			http.Error(response, "unexpected path", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	manifest, err := loadManifest(t.Context(), server.URL+"/releases/latest/download/"+manifestFileName)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != updateTestManifest().Version {
		t.Fatalf("manifest version = %q", manifest.Version)
	}
	want := []string{"/releases/latest/download/" + manifestFileName, "/releases/latest/download/" + manifestArchiveName}
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
	t.Helper()
	data, err := json.Marshal(updateTestManifest())
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
