package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	releasepkg "athena-launcher/internal/release"
)

func main() {
	input := flag.String("input", "release-manifest.json", "unsigned release manifest")
	output := flag.String("output", "release-manifest.json", "signed release manifest")
	publicOutput := flag.String("public-key-output", "release-public-key.txt", "release public key output")
	flag.Parse()
	privateKey, err := releasepkg.DecodePrivateKey(os.Getenv("ATHENA_RELEASE_PRIVATE_KEY"))
	if err != nil {
		fatal(err)
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		fatal(err)
	}
	var manifest releasepkg.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		fatal(err)
	}
	if err := manifest.Sign(privateKey); err != nil {
		fatal(err)
	}
	if err := manifest.Validate(currentPlatform(manifest)); err != nil {
		fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if err := manifest.Verify(publicKey, time.Now().UTC()); err != nil {
		fatal(err)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := atomicWrite(*output, append(encoded, '\n'), 0o600); err != nil {
		fatal(err)
	}
	publicValue := base64.RawStdEncoding.EncodeToString(publicKey)
	if err := atomicWrite(*publicOutput, []byte(publicValue+"\n"), 0o600); err != nil {
		fatal(err)
	}
	fmt.Printf("signed %s with release key %s\n", *output, releasepkg.ReleaseKeyID(publicKey))
}

func currentPlatform(manifest releasepkg.Manifest) string {
	for platform := range manifest.Database.Artifacts {
		return platform
	}
	return ""
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "release manifest signing failed:", strings.TrimSpace(err.Error()))
	os.Exit(1)
}
