package deployment

import (
	"context"
	"fmt"
	"os"
)

func prepare(ctx context.Context, opts options) (*Manifest, *launcherState, map[string]string, error) {
	state, err := loadState(opts.home)
	if err != nil {
		return nil, nil, nil, err
	}
	if deploymentFromState(state).Mode == connectionModeRemote {
		manifest, _, err := prepareRemote(ctx, opts, nil, nil, true, state)
		return manifest, state, map[string]string{}, err
	}
	return prepareWithTracker(ctx, opts, nil, nil, true)
}

func prepareWithTracker(ctx context.Context, opts options, tracker *startupTracker, control *startupController, updateApproved bool) (*Manifest, *launcherState, map[string]string, error) {
	if err := os.MkdirAll(opts.home, 0o700); err != nil {
		return nil, nil, nil, err
	}
	tracker.begin("manifest", "Reading release manifest")
	state, err := loadState(opts.home)
	if err != nil {
		tracker.fail("manifest", err)
		return nil, nil, nil, err
	}
	state.ManifestSource = opts.manifestSource
	manifest, err := loadManifest(ctx, opts.manifestSource)
	if err != nil {
		tracker.fail("manifest", err)
		return nil, nil, nil, err
	}
	tracker.complete("manifest", fmt.Sprintf("Release %s verified for %s", manifest.Version, platformKey()))
	if updates := packageUpdatesForOptions(opts, manifest); len(updates) > 0 && control != nil && !updateApproved {
		installedManifest, installedErr := loadInstalledManifest(opts.home)
		canDefer := installedErr == nil && installedPackagesUsable(opts.home, installedManifest)
		tracker.offerUpdate(updates, canDefer)
		var dismissUpdate <-chan struct{}
		if canDefer {
			dismissUpdate = control.dismissUpdate
		}
		select {
		case <-ctx.Done():
			return nil, nil, nil, ctx.Err()
		case <-control.applyUpdate:
			tracker.applyingUpdate()
		case <-dismissUpdate:
			manifest = installedManifest
			tracker.clearUpdate("Update postponed; using installed packages")
			tracker.complete("manifest", fmt.Sprintf("Using installed release %s", manifest.Version))
		}
	}

	tracker.begin("database-package", "Checking the managed PostgreSQL package")
	if _, err := installDatabase(ctx, opts.home, manifest); err != nil {
		tracker.fail("database-package", err)
		return nil, nil, nil, err
	}
	tracker.complete("database-package", fmt.Sprintf("PostgreSQL %s is available", manifest.Database.Version))

	tracker.begin("browser-package", "Checking the authenticated browser package")
	browserExecutable, err := installBrowser(ctx, opts.home, manifest, state)
	if err != nil {
		tracker.fail("browser-package", err)
		return nil, nil, nil, err
	}
	if browserExecutable == "" {
		tracker.complete("browser-package", "Using agent-browser from PATH when available")
	} else {
		tracker.complete("browser-package", fmt.Sprintf("Agent Browser %s is available", manifest.Browser.Version))
	}

	tracker.begin("services-package", "Checking Agent Runtime packages")
	executables, err := installServices(ctx, opts.home, manifest, state)
	if err != nil {
		tracker.fail("services-package", err)
		return nil, nil, nil, err
	}
	tracker.complete("services-package", fmt.Sprintf("%d runtime packages are available", len(manifest.Services)))
	if browserExecutable != "" {
		executables["agent-browser"] = browserExecutable
	}

	tracker.begin("frontend-package", "Checking the Athena interface package")
	if opts.frontendDir != "" {
		tracker.complete("frontend-package", fmt.Sprintf("Using local interface at %s", opts.frontendDir))
	} else {
		if _, err := installFrontend(ctx, opts.home, manifest, state); err != nil {
			tracker.fail("frontend-package", err)
			return nil, nil, nil, err
		}
		tracker.complete("frontend-package", "Athena interface package is available")
	}

	tracker.begin("configuration", "Generating local service configuration")
	if _, err := writeGeneratedConfigs(opts.home, state, executables); err != nil {
		tracker.fail("configuration", err)
		return nil, nil, nil, err
	}
	tracker.complete("configuration", "Local service configuration is ready")
	if err := saveInstalledManifest(opts.home, manifest); err != nil {
		tracker.fail("configuration", err)
		return nil, nil, nil, err
	}
	return manifest, state, executables, nil
}
