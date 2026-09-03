package deployment

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	releasepkg "athena-launcher/internal/release"
)

func TestSourceBuildIncludesPinnedReleasePublicKey(t *testing.T) {
	t.Setenv("ATHENA_RELEASE_PUBLIC_KEY", "")
	publicKey, err := configuredReleasePublicKey()
	if err != nil {
		t.Fatal(err)
	}
	want, err := releasepkg.DecodePublicKey(pinnedReleasePublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publicKey, want) {
		t.Fatal("source build does not use the pinned release public key")
	}
}

func TestRemoteManifestCannotReplacePinnedReleasePublicKeyFromEnvironment(t *testing.T) {
	otherPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATHENA_RELEASE_PUBLIC_KEY", base64.RawStdEncoding.EncodeToString(otherPublicKey))

	remoteKey, err := releasePublicKeyForSource("https://releases.example/release-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	pinnedKey, err := releasepkg.DecodePublicKey(DefaultReleasePublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(remoteKey, pinnedKey) {
		t.Fatal("remote manifest replaced the pinned trust root")
	}

	localKey, err := releasePublicKeyForSource("/tmp/release-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(localKey, otherPublicKey) {
		t.Fatal("local signed manifest did not use the explicit development key")
	}
}

func TestConfiguredDatabasePort(t *testing.T) {
	t.Setenv(envDatabasePort, "25432")
	if got := databasePort(); got != 25432 {
		t.Fatalf("databasePort() = %d, want 25432", got)
	}
	if err := validateRuntimeOverrides(); err != nil {
		t.Fatalf("validateRuntimeOverrides() error = %v", err)
	}
}

func TestConfiguredDatabasePortRejectsInvalidValue(t *testing.T) {
	for _, value := range []string{"0", "65536", "not-a-port"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(envDatabasePort, value)
			if err := validateRuntimeOverrides(); err == nil {
				t.Fatalf("validateRuntimeOverrides() accepted %q", value)
			}
		})
	}
}
