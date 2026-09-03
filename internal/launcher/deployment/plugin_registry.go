package deployment

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	pluginv1 "github.com/good-fish-man/athena-protocol/protocol/plugin/v1"
)

type pluginRegistryPaths struct {
	root       string
	packages   string
	registry   string
	trustStore string
	audit      string
	privateKey string
}

type localSigningKey struct {
	Schema     string `json:"schema"`
	KeyID      string `json:"key_id"`
	Algorithm  string `json:"algorithm"`
	PrivateKey string `json:"private_key"`
}

type localTrustStore struct {
	Schema string          `json:"schema"`
	Keys   []localTrustKey `json:"keys"`
}

type localTrustKey struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
	Disabled  bool   `json:"disabled"`
}

func ensurePluginRegistry(home string) (pluginRegistryPaths, error) {
	root := filepath.Join(home, "plugins")
	paths := pluginRegistryPaths{
		root: root, packages: filepath.Join(root, "packages"), registry: filepath.Join(root, "registry.json"),
		trustStore: filepath.Join(root, "trust-store.json"), audit: filepath.Join(root, "logs", "invocations.jsonl"),
		privateKey: filepath.Join(root, "signing", "private-key.json"),
	}
	for _, directory := range []string{paths.packages, filepath.Dir(paths.audit), filepath.Dir(paths.privateKey)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return paths, fmt.Errorf("create plugin registry directory: %w", err)
		}
	}
	if _, err := os.Stat(paths.registry); errors.Is(err, os.ErrNotExist) {
		data, _ := json.MarshalIndent(pluginv1.RegistryIndex{Schema: pluginv1.Schema, Entries: []pluginv1.RegistryEntry{}}, "", "  ")
		if err := writeAtomic(paths.registry, data, 0o600); err != nil {
			return paths, fmt.Errorf("initialize plugin registry: %w", err)
		}
	} else if err != nil {
		return paths, fmt.Errorf("inspect plugin registry: %w", err)
	}
	trustExists := fileExists(paths.trustStore)
	privateExists := fileExists(paths.privateKey)
	if trustExists != privateExists {
		return paths, fmt.Errorf("plugin signing identity is incomplete; restore both %s and %s from backup", paths.privateKey, paths.trustStore)
	}
	if !trustExists && !privateExists {
		if err := createLocalPluginSigningIdentity(paths); err != nil {
			return paths, err
		}
	}
	return paths, nil
}

func createLocalPluginSigningIdentity(paths pluginRegistryPaths) error {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate local plugin signing key: %w", err)
	}
	digest := sha256.Sum256(publicKey)
	keyID := "athena-local-" + hex.EncodeToString(digest[:8])
	privateDocument := localSigningKey{Schema: "athena.plugin-signing-key.v1", KeyID: keyID, Algorithm: pluginv1.SignatureEd25519, PrivateKey: base64.StdEncoding.EncodeToString(privateKey)}
	trustDocument := localTrustStore{Schema: pluginv1.TrustStoreSchema, Keys: []localTrustKey{{KeyID: keyID, Algorithm: pluginv1.SignatureEd25519, PublicKey: base64.StdEncoding.EncodeToString(publicKey)}}}
	privateData, _ := json.MarshalIndent(privateDocument, "", "  ")
	trustData, _ := json.MarshalIndent(trustDocument, "", "  ")
	if err := writeAtomic(paths.privateKey, privateData, 0o600); err != nil {
		return fmt.Errorf("write local plugin signing key: %w", err)
	}
	if err := writeAtomic(paths.trustStore, trustData, 0o600); err != nil {
		_ = os.Remove(paths.privateKey)
		return fmt.Errorf("write plugin trust store: %w", err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
