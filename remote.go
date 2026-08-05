package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

func runRemoteManaged(ctx context.Context, opts options, tracker *startupTracker, control *startupController, updateApproved bool, state *launcherState) error {
	selection, err := normalizeDeployment(deploymentFromState(state))
	if err != nil || selection.Mode != connectionModeRemote {
		return fmt.Errorf("invalid remote deployment configuration")
	}
	manifest, frontendPath, err := prepareRemote(ctx, opts, tracker, control, updateApproved, state)
	if err != nil {
		return err
	}
	state.LauncherPID = os.Getpid()
	if err := saveState(opts.home, state); err != nil {
		return err
	}
	defer func() {
		state.LauncherPID = 0
		_ = saveState(opts.home, state)
	}()

	tracker.begin("database", "Remote mode does not start a local database")
	tracker.complete("database", "Skipped in remote mode")
	tracker.begin("agent-runtime", "Remote mode uses the server Runtime")
	tracker.complete("agent-runtime", "Skipped in remote mode")
	tracker.begin("agent-runtime-client", "Checking the remote Runtime Client")
	if err := checkRemoteClient(ctx, selection.RemoteURL); err != nil {
		tracker.fail("agent-runtime-client", err)
		return err
	}
	tracker.complete("agent-runtime-client", "Remote Runtime Client is healthy")

	tracker.begin("frontend", "Starting the Athena interface")
	var frontend *frontendServer
	frontendURL := "/"
	if opts.desktop != nil {
		if err := opts.desktop.UseFrontend(frontendPath); err != nil {
			tracker.fail("frontend", err)
			return err
		}
		tracker.complete("frontend", "Athena interface is connected to the remote service")
	} else {
		frontend, err = startFrontendServer(manifest, frontendPath)
		if err != nil {
			tracker.fail("frontend", err)
			return err
		}
		frontendURL = "http://" + frontend.address + "/?runtime_client=" + url.QueryEscape(selection.RemoteURL)
		tracker.complete("frontend", "Athena browser interface is connected to the remote service")
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := frontend.Stop(stopCtx); err != nil {
				fmt.Fprintln(os.Stderr, "[frontend]", err)
			}
		}()
	}
	tracker.clearUpdate("Remote connection and Athena UI are current")
	tracker.ready(frontendURL)
	return waitRemoteMode(ctx, opts, tracker, control)
}

func prepareRemote(ctx context.Context, opts options, tracker *startupTracker, control *startupController, updateApproved bool, state *launcherState) (*Manifest, string, error) {
	if err := os.MkdirAll(opts.home, 0o700); err != nil {
		return nil, "", err
	}
	tracker.begin("manifest", "Reading release manifest for the Athena interface")
	state.ManifestSource = opts.manifestSource
	manifest, err := loadManifest(ctx, opts.manifestSource)
	if err != nil {
		tracker.fail("manifest", err)
		return nil, "", err
	}
	if manifest.Frontend == nil {
		err := fmt.Errorf("release manifest does not provide an Athena interface package")
		tracker.fail("frontend-package", err)
		return nil, "", err
	}
	tracker.complete("manifest", fmt.Sprintf("Release %s verified for %s", manifest.Version, platformKey()))
	if updates := frontendUpdatesForOptions(opts, manifest); len(updates) > 0 && control != nil && !updateApproved {
		installedManifest, installedErr := loadInstalledManifest(opts.home)
		canDefer := installedErr == nil && installedFrontendUsable(opts.home, installedManifest)
		tracker.offerUpdate(updates, canDefer)
		var dismiss <-chan struct{}
		if canDefer {
			dismiss = control.dismissUpdate
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-control.applyUpdate:
			tracker.applyingUpdate()
		case <-dismiss:
			manifest = installedManifest
			tracker.clearUpdate("UI update postponed")
			tracker.complete("manifest", fmt.Sprintf("Using installed UI release %s", manifest.Version))
		}
	}

	tracker.begin("database-package", "Remote mode does not require embedded PostgreSQL")
	tracker.complete("database-package", "Skipped in remote mode")
	tracker.begin("browser-package", "Checking the local browser controller package")
	browserExecutable, browserErr := installBrowser(ctx, opts.home, manifest, state)
	if browserErr != nil {
		tracker.complete("browser-package", fmt.Sprintf("Agent Browser unavailable; system browser fallback is enabled: %v", browserErr))
	} else if browserExecutable == "" || manifest.Browser == nil {
		tracker.complete("browser-package", "No Agent Browser package configured; system browser fallback is enabled")
	} else {
		tracker.complete("browser-package", fmt.Sprintf("Agent Browser %s is available for desktop control", manifest.Browser.Version))
	}
	tracker.begin("services-package", "Remote mode does not install Runtime services")
	tracker.complete("services-package", "Skipped in remote mode")
	tracker.begin("frontend-package", "Checking the Athena interface package")
	frontendPath := opts.frontendDir
	if frontendPath != "" {
		tracker.complete("frontend-package", fmt.Sprintf("Using local interface at %s", frontendPath))
	} else {
		frontendPath, err = installFrontend(ctx, opts.home, manifest, state)
		if err != nil {
			tracker.fail("frontend-package", err)
			return nil, "", err
		}
		tracker.complete("frontend-package", "Athena interface package is available")
	}
	tracker.begin("configuration", "Saving remote connection settings")
	if err := saveState(opts.home, state); err != nil {
		tracker.fail("configuration", err)
		return nil, "", err
	}
	if err := saveInstalledManifest(opts.home, manifest); err != nil {
		tracker.fail("configuration", err)
		return nil, "", err
	}
	tracker.complete("configuration", "Remote connection settings are ready")
	return manifest, frontendPath, nil
}

func checkRemoteClient(ctx context.Context, baseURL string) error {
	return checkRemoteClientWithHTTPClient(ctx, baseURL, &http.Client{Timeout: 12 * time.Second})
}

func checkRemoteClientWithHTTPClient(ctx context.Context, baseURL string, client *http.Client) error {
	requestCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, baseURL+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("build remote health request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("remote agent-runtime-client is unreachable at %s: %w", baseURL, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("remote agent-runtime-client health check returned HTTP %d", response.StatusCode)
	}
	return nil
}

func waitRemoteMode(ctx context.Context, opts options, tracker *startupTracker, control *startupController) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-control.checkUpdate:
			tracker.checkingForUpdates()
			manifest, err := loadManifest(ctx, opts.manifestSource)
			if err != nil {
				tracker.updateError(err)
				continue
			}
			updates := frontendUpdatesForOptions(opts, manifest)
			if len(updates) == 0 {
				tracker.clearUpdate("Athena UI is current")
				continue
			}
			tracker.offerUpdate(updates, true)
		case <-control.dismissUpdate:
			tracker.clearUpdate("UI update postponed")
		case <-control.applyUpdate:
			tracker.applyingUpdate()
			return errUpdateRequested
		}
	}
}
