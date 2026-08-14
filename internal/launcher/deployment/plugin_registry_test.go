package deployment

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	pluginv1 "github.com/good-fish-man/athena-protocol/protocol/plugin/v1"
)

func TestPluginRegistryProvisioningIsStable(t *testing.T) {
	home := t.TempDir()
	paths, err := ensurePluginRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	var index pluginv1.RegistryIndex
	readJSONFile(t, paths.registry, &index)
	if index.Schema != pluginv1.Schema || len(index.Entries) != 0 {
		t.Fatalf("unexpected initial Registry: %+v", index)
	}
	var private localSigningKey
	var trust localTrustStore
	readJSONFile(t, paths.privateKey, &private)
	readJSONFile(t, paths.trustStore, &trust)
	if len(trust.Keys) != 1 || trust.Keys[0].KeyID != private.KeyID {
		t.Fatalf("local signing identity is inconsistent")
	}
	privateBytes, _ := base64.StdEncoding.DecodeString(private.PrivateKey)
	publicBytes, _ := base64.StdEncoding.DecodeString(trust.Keys[0].PublicKey)
	if len(privateBytes) != ed25519.PrivateKeySize || len(publicBytes) != ed25519.PublicKeySize || !ed25519.PrivateKey(privateBytes).Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(publicBytes)) {
		t.Fatal("trust store does not contain the generated signing key")
	}
	firstKey := private.PrivateKey
	if _, err := ensurePluginRegistry(home); err != nil {
		t.Fatal(err)
	}
	readJSONFile(t, paths.privateKey, &private)
	if private.PrivateKey != firstKey {
		t.Fatal("launcher rotated the local signing key during normal startup")
	}
}

func TestGeneratedConfigsSharePluginRegistry(t *testing.T) {
	home := t.TempDir()
	paths, err := writeGeneratedConfigs(home, &launcherState{DBPassword: "secret", InternalServiceToken: "token"}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := os.ReadFile(paths.clientConfig)
	runtime, _ := os.ReadFile(paths.runtimeConfig)
	for _, value := range []string{string(client), string(runtime)} {
		if !strings.Contains(value, "registry.json") || !strings.Contains(value, "trust-store.json") || !strings.Contains(value, "invocations.jsonl") {
			t.Fatalf("generated service config does not share plugin paths:\n%s", value)
		}
	}
}

func TestPluginRegistryRejectsPartialSigningIdentity(t *testing.T) {
	home := t.TempDir()
	paths, err := ensurePluginRegistry(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.trustStore); err != nil {
		t.Fatal(err)
	}
	if _, err := ensurePluginRegistry(home); err == nil {
		t.Fatal("partial signing identity was silently replaced")
	}
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
