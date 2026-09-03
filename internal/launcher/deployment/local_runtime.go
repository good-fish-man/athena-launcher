package deployment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

func runForeground(opts options) error {
	ctx, cancel := signal.NotifyContext(context.Background(), terminationSignals()...)
	deviceDone, err := startHeadlessDeviceRuntime(ctx, opts)
	if err != nil {
		cancel()
		return err
	}
	defer func() {
		cancel()
		waitForDeviceRuntime(deviceDone, 20*time.Second)
	}()
	stopPath := filepath.Join(opts.home, "stop.request")
	_ = os.Remove(stopPath)
	go watchStopRequest(ctx, cancel, stopPath)
	tracker := newStartupTracker(opts.home)
	control := newStartupController()
	statusServer, err := startStartupServer(tracker, control)
	if err != nil {
		return err
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		_ = statusServer.Stop(stopCtx)
	}()

	updateApproved := false
	for {
		err = runManaged(ctx, opts, tracker, control, updateApproved)
		if err == nil {
			return nil
		}
		if errors.Is(err, errUpdateRequested) {
			tracker.protectingUpdate()
			backupCtx, backupCancel := context.WithTimeout(ctx, 15*time.Minute)
			state, stateErr := loadState(opts.home)
			var backupErr error
			switch {
			case stateErr != nil:
				backupErr = fmt.Errorf("load installation identity before update recovery point: %w", stateErr)
			case deploymentFromState(state).Mode == connectionModeRemote:
				// Remote mode updates only the local UI/browser shell and must never
				// inspect or mutate a server-side database.
				tracker.applyingUpdate()
			default:
				manifest, manifestErr := loadInstalledManifest(opts.home)
				if manifestErr != nil {
					backupErr = fmt.Errorf("load installed release before update recovery point: %w", manifestErr)
				} else {
					var recovery *managedRecoveryPoint
					recovery, backupErr = createManagedPostgresRecoveryPoint(backupCtx, opts.home, state.BackupEncryptionKey, manifest.Version)
					if backupErr == nil {
						fmt.Printf("[update] encrypted managed PostgreSQL recovery point created and verified: %s\n", recovery.BackupID)
					}
				}
			}
			backupCancel()
			if backupErr != nil {
				fmt.Fprintln(os.Stderr, "[update] pre-update recovery point failed:", backupErr)
				tracker.updateProtectionError(backupErr)
				updateApproved = false
				continue
			}
			updateApproved = true
			tracker.reset()
			tracker.applyingUpdate()
			continue
		}
		if tracker.current().State != "error" {
			tracker.fail("", err)
		}
		fmt.Fprintln(os.Stderr, "[startup]", err)
		select {
		case <-ctx.Done():
			return err
		case <-statusServer.retry:
			tracker.reset()
			continue
		case <-time.After(30 * time.Minute):
			return err
		}
	}
}

func startHeadlessDeviceRuntime(ctx context.Context, opts options) (<-chan struct{}, error) {
	state, err := loadState(opts.home)
	if err != nil {
		return nil, fmt.Errorf("load installation identity for device runtime: %w", err)
	}
	device, err := newDeviceRuntime(state, newDesktopBridgeWithState(opts.home, nil, state))
	if err != nil {
		return nil, fmt.Errorf("initialize headless device runtime: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		device.Run(ctx)
	}()
	return done, nil
}

func waitForDeviceRuntime(done <-chan struct{}, timeout time.Duration) {
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(timeout):
		fmt.Fprintln(os.Stderr, "[device-runtime] timed out while releasing browser sessions")
	}
}

func runManaged(ctx context.Context, opts options, tracker *startupTracker, control *startupController, updateApproved bool) error {
	currentState, err := loadState(opts.home)
	if err != nil {
		return err
	}
	if deploymentFromState(currentState).Mode == connectionModeRemote {
		return runRemoteManaged(ctx, opts, tracker, control, updateApproved, currentState)
	}
	manifest, state, executables, err := prepareWithTracker(ctx, opts, tracker, control, updateApproved)
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

	databaseDir, err := installDatabase(ctx, opts.home, manifest)
	if err != nil {
		tracker.fail("database", err)
		return err
	}
	database := newManagedDatabase(opts.home, databaseDir, manifest.Database.BinDir, state.DBPassword)
	fmt.Println("[postgres] preparing managed database")
	tracker.begin("database", "Starting the managed PostgreSQL database")
	if err := database.Start(ctx); err != nil {
		tracker.fail("database", err)
		return err
	}
	tracker.complete("database", fmt.Sprintf("PostgreSQL is ready on port %d", databasePort()))
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer stopCancel()
		if err := database.Stop(stopCtx); err != nil {
			fmt.Fprintln(os.Stderr, "[postgres]", err)
		}
	}()
	fmt.Printf("[postgres] ready at 127.0.0.1:%d/%s\n", databasePort(), defaultDatabaseName)

	paths, err := writeGeneratedConfigs(opts.home, state, executables)
	if err != nil {
		return err
	}
	supervisor := newSupervisor(opts.home, paths, manifest, executables, tracker, state.BrowserDataDir, state.BrowserEncryptionKey, state.InternalServiceToken, state.BootstrapAdminPassword)
	if err := supervisor.StartAll(ctx); err != nil {
		return err
	}
	defer supervisor.StopAll()
	frontendPath := ""
	if opts.frontendDir != "" {
		frontendPath = opts.frontendDir
	} else if manifest.Frontend != nil {
		frontendVersion := state.Installed["frontend"]
		if frontendVersion == "" {
			frontendVersion = manifest.Version
		}
		frontendPath = frontendRoot(filepath.Join(opts.home, "frontend", frontendVersion), manifest.Frontend.Root)
	}
	tracker.begin("frontend", "Starting the Athena interface")
	if opts.desktop != nil {
		if manifest.Frontend == nil {
			err := errors.New("desktop mode requires an Athena interface package")
			tracker.fail("frontend", err)
			return err
		}
		if err := opts.desktop.UseFrontend(frontendPath); err != nil {
			tracker.fail("frontend", err)
			return err
		}
		tracker.complete("frontend", "Athena interface is loaded in the desktop window")
		clearUpdateWhenCurrent(tracker)
		tracker.ready("/")
		fmt.Println("Athena desktop is ready")
		return runSupervisor(ctx, opts, supervisor, control)
	}

	frontend, err := startFrontendServer(manifest, frontendPath)
	if err != nil {
		tracker.fail("frontend", err)
		return err
	}
	frontendURL := fmt.Sprintf("http://127.0.0.1:%d/", defaultFrontendPort)
	if frontend != nil {
		frontendURL = "http://" + frontend.address + "/"
		tracker.complete("frontend", "Athena interface is accepting connections")
	} else {
		tracker.complete("frontend", "API-only mode is ready")
	}
	clearUpdateWhenCurrent(tracker)
	tracker.ready(frontendURL)
	if frontend != nil {
		defer func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopCancel()
			if err := frontend.Stop(stopCtx); err != nil {
				fmt.Fprintln(os.Stderr, "[frontend]", err)
			}
		}()
		fmt.Printf("Athena is ready: http://%s\n", frontend.address)
	} else {
		fmt.Printf("Athena API is ready: http://127.0.0.1:%d\n", defaultClientHTTPPort)
	}
	return runSupervisor(ctx, opts, supervisor, control)
}

func clearUpdateWhenCurrent(tracker *startupTracker) {
	if tracker != nil && tracker.current().Update.State != "available" {
		tracker.clearUpdate("All installed packages are current")
	}
}

func runSupervisor(ctx context.Context, opts options, supervisor *supervisor, control *startupController) error {
	return supervisor.Run(ctx, control, func() ([]packageUpdate, error) {
		remote, err := loadManifest(ctx, opts.manifestSource)
		if err != nil {
			return nil, err
		}
		return packageUpdatesForOptions(opts, remote), nil
	})
}
