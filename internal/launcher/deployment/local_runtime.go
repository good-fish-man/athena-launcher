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
	defer cancel()
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
	tracker.complete("database", fmt.Sprintf("PostgreSQL is ready on port %d", defaultDatabasePort))
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer stopCancel()
		if err := database.Stop(stopCtx); err != nil {
			fmt.Fprintln(os.Stderr, "[postgres]", err)
		}
	}()
	fmt.Printf("[postgres] ready at 127.0.0.1:%d/%s\n", defaultDatabasePort, defaultDatabaseName)

	paths, err := writeGeneratedConfigs(opts.home, state, executables)
	if err != nil {
		return err
	}
	supervisor := newSupervisor(opts.home, paths, manifest, executables, tracker, state.BrowserEncryptionKey, state.InternalServiceToken)
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
