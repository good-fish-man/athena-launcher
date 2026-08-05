package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDefaultManifestUsesLatestPublicRelease(t *testing.T) {
	t.Setenv("ATHENA_MANIFEST_URL", "")
	original := defaultManifestURL
	defaultManifestURL = publicManifestURL
	t.Cleanup(func() { defaultManifestURL = original })

	if got := defaultManifest(t.TempDir()); got != publicManifestURL {
		t.Fatalf("defaultManifest() = %q, want %q", got, publicManifestURL)
	}
}

func TestManifestValidation(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	manifest := &Manifest{
		Version: "1.0.0",
		Database: DatabaseSpec{
			Version:   "16.3",
			BinDir:    "bin",
			Artifacts: map[string]Artifact{platformKey(): {URL: "https://downloads.example/postgres.tar.gz", SHA256: checksum, Format: "tar.gz"}},
		},
		Services: []ServiceSpec{{
			Name: "agent-runtime", Order: 1,
			Artifacts: map[string]Artifact{platformKey(): {URL: "https://downloads.example/runtime.tar.gz", SHA256: checksum, Format: "tar.gz", Executable: "agent-runtime"}},
		}},
	}
	if err := manifest.validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	manifest.Browser = &BrowserSpec{Version: "0.33.1", Artifacts: map[string]Artifact{
		platformKey(): {URL: "https://downloads.example/agent-browser", SHA256: checksum, Format: "raw", Executable: "agent-browser"},
	}}
	if err := manifest.validate(); err != nil {
		t.Fatalf("manifest with browser artifact rejected: %v", err)
	}
	manifest.Database.Artifacts["test-missing-url"] = Artifact{SHA256: checksum, Format: "tar.gz"}
	if err := manifest.validate(); err == nil {
		t.Fatal("artifact without a URL was accepted")
	}
	delete(manifest.Database.Artifacts, "test-missing-url")
	manifest.Services[0].Artifacts[platformKey()] = Artifact{URL: "https://downloads.example/runtime", SHA256: "bad", Executable: "../runtime"}
	if err := manifest.validate(); err == nil {
		t.Fatal("unsafe manifest was accepted")
	}
}

func TestMacOSServicePathIncludesDesktopToolLocations(t *testing.T) {
	got := macOSServicePath("/usr/bin:/bin:/opt/homebrew/bin")
	want := "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin:/usr/bin:/bin"
	if got != want {
		t.Fatalf("macOSServicePath() = %q, want %q", got, want)
	}
}

func TestSetEnvironmentValueReplacesExistingValues(t *testing.T) {
	got := setEnvironmentValue([]string{"HOME=/tmp", "PATH=/bin", "PATH=/usr/bin"}, "PATH", "/opt/homebrew/bin:/bin")
	want := []string{"HOME=/tmp", "PATH=/opt/homebrew/bin:/bin"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("setEnvironmentValue() = %q, want %q", got, want)
	}
}

func TestSafeArchivePath(t *testing.T) {
	root := t.TempDir()
	if _, err := safeArchivePath(root, "bin/agent-runtime"); err != nil {
		t.Fatalf("safe path rejected: %v", err)
	}
	if got, err := safeArchivePath(root, "./"); err != nil || got != root {
		t.Fatalf("archive root rejected: path=%q err=%v", got, err)
	}
	for _, value := range []string{"../escape", "bin/../../escape", "/absolute/path"} {
		if _, err := safeArchivePath(root, value); err == nil {
			t.Fatalf("unsafe path %q was accepted", value)
		}
	}
}

func TestExtractTarGZAllowsRootDirectoryEntry(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "postgres.tar.gz")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(archiveFile)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []struct {
		header  *tar.Header
		content string
	}{
		{header: &tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: &tar.Header{Name: "./bin/postgres", Typeflag: tar.TypeReg, Mode: 0o755, Size: 8}, content: "postgres"},
		{header: &tar.Header{Name: "./lib/libicu.68.2.dylib", Typeflag: tar.TypeReg, Mode: 0o755, Size: 3}, content: "icu"},
		{header: &tar.Header{Name: "./lib/libicu.68.dylib", Typeflag: tar.TypeSymlink, Mode: 0o755, Linkname: "libicu.68.2.dylib"}},
	}
	for _, entry := range entries {
		if err := tarWriter.WriteHeader(entry.header); err != nil {
			t.Fatal(err)
		}
		if entry.content != "" {
			if _, err := tarWriter.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	if err := extractTarGZ(archivePath, target); err != nil {
		t.Fatalf("extract archive with root entry: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(target, "bin", "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "postgres" {
		t.Fatalf("extracted content = %q", content)
	}
	link := filepath.Join(target, "lib", "libicu.68.dylib")
	if linkTarget, err := os.Readlink(link); err != nil || linkTarget != "libicu.68.2.dylib" {
		t.Fatalf("extracted symlink target = %q, err=%v", linkTarget, err)
	}
	linkedContent, err := os.ReadFile(link)
	if err != nil || string(linkedContent) != "icu" {
		t.Fatalf("read extracted symlink: content=%q err=%v", linkedContent, err)
	}
}

func TestExtractTarGZRejectsEscapingSymlink(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "malicious.tar.gz")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(archiveFile)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{Name: "bin/escape", Typeflag: tar.TypeSymlink, Mode: 0o755, Linkname: "../../outside"}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGZ(archivePath, t.TempDir()); err == nil {
		t.Fatal("escaping archive symlink was accepted")
	}
}

func TestDatabaseArtifactMarkerIsVersioned(t *testing.T) {
	if got := databaseArtifactMarker("ABC123"); got != "2:abc123" {
		t.Fatalf("databaseArtifactMarker() = %q", got)
	}
}

func TestInstallRawArtifactAndChecksum(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "source")
	content := []byte("fake service binary")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	target := filepath.Join(home, "services", "test", "1.0.0")
	artifact := Artifact{URL: source, SHA256: hex.EncodeToString(sum[:]), Format: "raw", Executable: "test-service"}
	if err := installArtifact(context.Background(), home, target, artifact); err != nil {
		t.Fatalf("install artifact: %v", err)
	}
	installed, err := os.ReadFile(filepath.Join(target, "test-service"))
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(content) {
		t.Fatalf("installed content = %q", installed)
	}
	artifact.SHA256 = strings.Repeat("0", 64)
	if err := installArtifact(context.Background(), home, target, artifact); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
}

func TestInstallBrowserUsesVerifiedRawArtifact(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "agent-browser-source")
	content := []byte("fake browser binary")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	manifest := &Manifest{Browser: &BrowserSpec{
		Version: "0.33.1",
		Artifacts: map[string]Artifact{platformKey(): {
			URL: source, SHA256: hex.EncodeToString(sum[:]), Format: "raw", Executable: "agent-browser",
		}},
	}}
	state := &launcherState{Installed: make(map[string]string), DBPassword: "test"}
	executable, err := installBrowser(context.Background(), home, manifest, state)
	if err != nil {
		t.Fatal(err)
	}
	if executable != filepath.Join(home, "browser", "0.33.1", "agent-browser") {
		t.Fatalf("installed executable = %q", executable)
	}
	if state.Installed["agent-browser"] != "0.33.1" {
		t.Fatalf("browser version was not saved: %+v", state.Installed)
	}
}

func TestGeneratedConfigsUseManagedDatabase(t *testing.T) {
	home := t.TempDir()
	state := &launcherState{DBPassword: "test-secret"}
	executables := map[string]string{"agent-runtime": filepath.Join(home, "services", "agent-runtime", "1", "agent-runtime")}
	paths, err := writeGeneratedConfigs(home, state, executables)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.clientConfig, paths.runtimeConfig} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "test-secret") || !strings.Contains(text, "db_port: 15432") {
			t.Fatalf("managed database settings missing from %s", path)
		}
	}
	skills, err := os.ReadFile(paths.skillsConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(skills) != "skills: {}\n" {
		t.Fatalf("unexpected skills config: %q", skills)
	}
}

func TestManagedDatabaseRequiresOnlyEmbeddedServerBinaries(t *testing.T) {
	installDir := t.TempDir()
	binDir := filepath.Join(installDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, name := range []string{"initdb", "pg_ctl", "postgres"} {
		if err := os.WriteFile(filepath.Join(binDir, name+suffix), nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	database := newManagedDatabase(t.TempDir(), installDir, "bin", "secret")
	if err := database.validateBinaries(); err != nil {
		t.Fatalf("minimal embedded PostgreSQL package rejected: %v", err)
	}
}

func TestManagedServicePorts(t *testing.T) {
	manifest := &Manifest{Services: []ServiceSpec{
		{Name: "agent-runtime"},
		{Name: "agent-runtime-client"},
		{Name: "agent-browser"},
	}}
	ports := managedServicePorts(manifest)
	got := make(map[string]bool)
	for _, port := range ports {
		got[port.Service+":"+port.Reason+":"+strconv.Itoa(port.Port)] = true
	}
	for _, want := range []string{
		"agent-runtime:gRPC:18080",
		"agent-runtime:health:18081",
		"agent-runtime-client:HTTP/API:8090",
	} {
		if !got[want] {
			t.Fatalf("managed service port %s missing from %+v", want, ports)
		}
	}
	if len(ports) != 3 {
		t.Fatalf("managedServicePorts() returned extra ports: %+v", ports)
	}
}

func TestPortAvailableDetectsWildcardListener(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if portAvailable(uint32(port)) {
		t.Fatalf("portAvailable(%d) returned true while wildcard listener is active", port)
	}
}

func TestAthenaManagedProcessDetection(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".athena")
	servicePath := filepath.Join(home, "services", "agent-runtime", "0.1.3", "agent-runtime")
	if !isAthenaManagedProcess(home, "agent-runtime", portOwner{PID: 42, Command: "agent-runtime", Args: servicePath}) {
		t.Fatal("installed Athena service process was not recognized")
	}
	if !isAthenaManagedProcess(home, "agent-runtime", portOwner{PID: 43, Command: "___go_build_agent_runtime", Args: ""}) {
		t.Fatal("Go development runtime process was not recognized")
	}
	if isAthenaManagedProcess(home, "agent-runtime", portOwner{PID: 44, Command: "postgres", Args: "/usr/local/bin/postgres"}) {
		t.Fatal("unrelated process was incorrectly recognized as Athena-managed")
	}
}

func TestQuoteIdentifier(t *testing.T) {
	if got := quoteIdentifier(`agent"runtime`); got != `"agent""runtime"` {
		t.Fatalf("quoteIdentifier() = %q", got)
	}
}

func TestPlainHTTPOnlyAllowedForLoopback(t *testing.T) {
	remote, _ := url.Parse("http://downloads.example/file")
	if err := validateDownloadURL(remote); err == nil {
		t.Fatal("plain HTTP remote URL was accepted")
	}
	local, _ := url.Parse("http://127.0.0.1:8080/file")
	if err := validateDownloadURL(local); err != nil {
		t.Fatalf("loopback URL rejected: %v", err)
	}
}

func TestSPAHandler(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("athena app"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("console.log('athena')"), 0o600); err != nil {
		t.Fatal(err)
	}

	for path, expected := range map[string]string{"/agents/123": "athena app", "/assets/app.js": "console.log('athena')"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		spaHandler(root).ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("GET %s returned %d: %s", path, response.Code, response.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/.artifact-sha256", nil)
	response := httptest.NewRecorder()
	spaHandler(root).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("dotfile response status = %d", response.Code)
	}
}
