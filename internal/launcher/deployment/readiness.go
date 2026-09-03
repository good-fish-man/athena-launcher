package deployment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ga "github.com/good-fish-man/athena-protocol/protocol/ga/v1"
)

func launcherReadiness(home string) ga.ReadinessReport {
	checks := make([]ga.ReadinessCheck, 0, 7)
	state, stateErr := loadState(home)
	instanceID := "athena-launcher"
	if stateErr != nil {
		checks = append(checks, launcherCheck("identity.recovery", "security", ga.StatusFail, stateErr.Error()))
	} else {
		instanceID = state.DeviceID
		if state.DBPassword == "" || state.BrowserEncryptionKey == "" || state.BackupEncryptionKey == "" || state.InternalServiceToken == "" || state.BootstrapAdminPassword == "" || state.DeviceID == "" {
			checks = append(checks, launcherCheck("identity.recovery", "security", ga.StatusFail, "installation identity is incomplete"))
		} else {
			checks = append(checks, launcherCheck("identity.recovery", "security", ga.StatusPass, "installation identity is mirrored in protected recovery secrets"))
		}
	}

	manifest, manifestErr := loadInstalledManifest(home)
	if manifestErr != nil {
		checks = append(checks, launcherCheck("release.manifest", "compatibility", ga.StatusFail, manifestErr.Error()))
	} else if err := manifest.ValidateGA(); err != nil {
		checks = append(checks, launcherCheck("release.manifest", "compatibility", ga.StatusFail, err.Error()))
	} else if compareReleaseSemver(manifest.Version, ga.ReleaseVersion) < 0 {
		checks = append(checks, launcherCheck("release.manifest", "compatibility", ga.StatusFail, "installed release is older than GA v1.0"))
	} else {
		checks = append(checks, ga.ReadinessCheck{
			ID: "release.manifest", Category: "compatibility", Status: ga.StatusPass, Required: true,
			Message:  "signed release manifest pins all GA component versions and hashes",
			Evidence: []ga.EvidenceRef{{Kind: "compatibility-matrix", Reference: manifest.CompatibilityURL, SHA256: manifest.CompatibilitySHA256}},
		})
	}

	if stateErr == nil && manifestErr == nil {
		if deploymentFromState(state).Mode == connectionModeRemote {
			if strings.TrimSpace(state.RemoteClientURL) == "" {
				checks = append(checks, launcherCheck("installation.mode", "deployment", ga.StatusFail, "remote mode has no control-plane URL"))
			} else if !installedFrontendUsable(home, manifest) {
				checks = append(checks, launcherCheck("installation.mode", "deployment", ga.StatusFail, "remote mode frontend package is incomplete"))
			} else {
				checks = append(checks, launcherCheck("installation.mode", "deployment", ga.StatusPass, "remote mode keeps runtime services external and retains the local desktop shell"))
			}
		} else if err := validateInstalledPackages(home, manifest); err != nil {
			checks = append(checks, launcherCheck("installation.mode", "deployment", ga.StatusFail, err.Error()))
		} else {
			checks = append(checks, launcherCheck("installation.mode", "deployment", ga.StatusPass, "local mode packages and embedded PostgreSQL are complete"))
		}
	}

	if stateErr == nil && strings.TrimSpace(state.BackupEncryptionKey) != "" {
		checks = append(checks, launcherCheck("recovery.key", "recovery", ga.StatusPass, "backup encryption key is protected independently from state.json"))
	} else {
		checks = append(checks, launcherCheck("recovery.key", "recovery", ga.StatusFail, "backup encryption identity is unavailable"))
	}
	logicalBackupCount, logicalBackupErr := countBackupManifests(filepath.Join(home, "backups", "logical"))
	managedBackupCount := 0
	managedBackupErr := error(nil)
	if stateErr != nil || strings.TrimSpace(state.BackupEncryptionKey) == "" {
		managedBackupErr = fmt.Errorf("managed recovery points cannot be authenticated without the installation backup key")
	} else {
		managedBackupCount, managedBackupErr = authenticatedManagedRecoveryInventory(home, state.BackupEncryptionKey)
	}
	backupErr := logicalBackupErr
	if backupErr == nil {
		backupErr = managedBackupErr
	}
	if backupErr != nil {
		checks = append(checks, launcherCheck("recovery.backup", "recovery", ga.StatusFail, backupErr.Error()))
	} else if managedBackupCount > 0 {
		checks = append(checks, launcherCheck("recovery.backup", "recovery", ga.StatusPass, fmt.Sprintf("%d authenticated managed recovery point(s) are retained (%d logical backup manifest(s) require control-plane verification)", managedBackupCount, logicalBackupCount)))
	} else if logicalBackupCount > 0 {
		checks = append(checks, launcherCheck("recovery.backup", "recovery", ga.StatusExternalRequired, fmt.Sprintf("%d logical backup manifest(s) exist; verify them through the control plane before GA release", logicalBackupCount)))
	} else {
		checks = append(checks, launcherCheck("recovery.backup", "recovery", ga.StatusExternalRequired, "create and verify an encrypted backup before GA release"))
	}

	if manifestErr == nil {
		status, message := platformSigningReadiness(manifest)
		checks = append(checks, launcherCheck("platform.signing", "supply-chain", status, message))
	}
	checks = append(checks, launcherCheck("frontend.independent", "durability", ga.StatusPass, "launcher, device runtime, and managed services continue without the frontend window"))

	return ga.ReadinessReport{
		Schema: ga.Schema, ReleaseVersion: LauncherVersion, Component: "athena-launcher",
		InstanceID: instanceID, Status: launcherAggregateStatus(checks), Checks: checks, ObservedAt: time.Now().UTC(),
	}
}

func countBackupManifests(directory string) (int, error) {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read backup inventory: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(directory, entry.Name(), "manifest.json")); err == nil {
				count++
			}
		}
	}
	return count, nil
}

func authenticatedManagedRecoveryInventory(home, encodedKey string) (int, error) {
	directory := filepath.Join(home, "recovery", "managed-postgres")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read managed recovery inventory: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("managed recovery inventory contains a symbolic link: %s", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		if !managedRecoveryIDPattern.MatchString(entry.Name()) {
			if _, statErr := os.Stat(filepath.Join(directory, entry.Name(), "manifest.json")); statErr == nil {
				return 0, fmt.Errorf("managed recovery inventory contains an invalid recovery point id: %s", entry.Name())
			}
			continue
		}
		if _, err := inspectManagedPostgresRecoveryPoint(home, encodedKey, entry.Name()); err != nil {
			return 0, fmt.Errorf("authenticate managed recovery point %s: %w", entry.Name(), err)
		}
		count++
	}
	return count, nil
}

func platformSigningReadiness(manifest *Manifest) (string, string) {
	if manifest == nil || manifest.Development {
		return ga.StatusExternalRequired, "production release signature and platform notarization must be verified externally"
	}
	statuses := []string{manifest.Database.Artifacts[platformKey()].CodeSigning}
	if manifest.Browser != nil {
		statuses = append(statuses, manifest.Browser.Artifacts[platformKey()].CodeSigning)
	}
	for _, service := range manifest.Services {
		statuses = append(statuses, service.Artifacts[platformKey()].CodeSigning)
	}
	if manifest.Frontend != nil {
		statuses = append(statuses, manifest.Frontend.Artifacts[platformKey()].CodeSigning)
	}
	for _, status := range statuses {
		normalized := strings.ToUpper(strings.TrimSpace(status))
		if normalized == "" || normalized == "UNAVAILABLE" || normalized == "UNSIGNED" {
			return ga.StatusExternalRequired, "one or more platform artifacts still require signing or notarization"
		}
	}
	return ga.StatusPass, "manifest and platform signing evidence are present for every installed artifact"
}

func launcherCheck(id, category, status, message string) ga.ReadinessCheck {
	return ga.ReadinessCheck{ID: id, Category: category, Status: status, Required: true, Message: message}
}

func launcherAggregateStatus(checks []ga.ReadinessCheck) string {
	status := ga.StatusPass
	for _, check := range checks {
		if !check.Required {
			continue
		}
		switch check.Status {
		case ga.StatusFail:
			return ga.StatusFail
		case ga.StatusBlocked:
			status = ga.StatusBlocked
		case ga.StatusExternalRequired:
			if status == ga.StatusPass {
				status = ga.StatusExternalRequired
			}
		case ga.StatusNotRun:
			if status == ga.StatusPass {
				status = ga.StatusNotRun
			}
		}
	}
	return status
}
