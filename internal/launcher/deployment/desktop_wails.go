//go:build desktop

package deployment

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	wailsoptions "github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const desktopInstanceID = "5c469778-cfb4-4cb1-aef7-a2d526fe57f4"

type desktopApplication struct {
	opts    options
	tracker *startupTracker
	control *startupController
	retry   chan struct{}
	assets  *desktopAssetSwitch

	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	deviceDone chan struct{}
	update     string
	device     *deviceRuntime
	bridge     *desktopBridge
}

func launchDesktop(opts options) error {
	if err := stopLegacyLauncher(opts.home); err != nil {
		return err
	}
	if err := takeOverOlderLauncher(opts.home); err != nil {
		return err
	}
	logFile, err := redirectDesktopLogs(opts.home)
	if err != nil {
		return err
	}
	defer logFile.Close()

	tracker := newStartupTracker(opts.home)
	control := newStartupController()
	state, stateErr := loadState(opts.home)
	selection, selectionErr := savedDeploymentSelection(state)
	needsDeployment := deploymentSelectionRequired(state, stateErr, selectionErr)
	if needsDeployment {
		tracker.awaitDeployment(deploymentFromState(state))
		if stateErr != nil {
			tracker.deploymentError(stateErr)
		} else {
			tracker.deploymentError(selectionErr)
		}
	} else {
		tracker.deploymentConfigured(selection)
	}
	retry := make(chan struct{}, 1)
	assets := newDesktopAssetSwitch(tracker, retry, control)
	opts.desktop = assets
	app := &desktopApplication{
		opts:    opts,
		tracker: tracker,
		control: control,
		retry:   retry,
		assets:  assets,
		done:    make(chan struct{}),
	}
	bridge := newDesktopBridgeWithState(opts.home, func() (string, error) {
		app.mu.Lock()
		ctx := app.ctx
		app.mu.Unlock()
		if ctx == nil {
			return "", fmt.Errorf("desktop window is not ready")
		}
		return wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{Title: "Authorize a folder for Athena"})
	}, state)
	assets.SetDesktopBridge(bridge)
	app.bridge = bridge
	tracker.listen(app.handleSnapshot)
	if !needsDeployment {
		control.deployment <- selection
	}

	return wails.Run(&wailsoptions.App{
		Title:             "Athena",
		Width:             1440,
		Height:            920,
		MinWidth:          1024,
		MinHeight:         700,
		HideWindowOnClose: true,
		BackgroundColour:  &wailsoptions.RGBA{R: 238, G: 249, B: 254, A: 255},
		AssetServer:       &assetserver.Options{Handler: http.HandlerFunc(assets.ServeHTTP)},
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		SingleInstanceLock: &wailsoptions.SingleInstanceLock{
			UniqueId: desktopInstanceID,
			OnSecondInstanceLaunch: func(wailsoptions.SecondInstanceData) {
				app.showUpdateCheck()
			},
		},
	})
}

func (a *desktopApplication) startup(ctx context.Context) {
	serviceCtx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.ctx = ctx
	a.cancel = cancel
	a.mu.Unlock()
	go a.run(serviceCtx)
}

func (a *desktopApplication) run(ctx context.Context) {
	defer close(a.done)
	stopPath := filepath.Join(a.opts.home, "stop.request")
	_ = os.Remove(stopPath)
	go watchStopRequest(ctx, a.requestShutdown, stopPath)
	select {
	case <-ctx.Done():
		return
	case <-a.control.deployment:
	}
	a.startDeviceRuntime(ctx)

	updateApproved := false
	for {
		err := runManaged(ctx, a.opts, a.tracker, a.control, updateApproved)
		if err == nil || ctx.Err() != nil {
			return
		}
		if err == errUpdateRequested {
			updateApproved = true
			a.showStartup()
			a.tracker.reset()
			a.tracker.applyingUpdate()
			continue
		}
		if a.tracker.current().State != "error" {
			a.tracker.fail("", err)
		}
		fmt.Fprintln(os.Stderr, "[desktop-startup]", err)
		select {
		case <-ctx.Done():
			return
		case <-a.retry:
			updateApproved = false
			a.showStartup()
			a.tracker.reset()
		}
	}
}

func (a *desktopApplication) startDeviceRuntime(ctx context.Context) {
	a.mu.Lock()
	if a.device != nil {
		a.mu.Unlock()
		return
	}
	bridge := a.bridge
	a.mu.Unlock()
	state, err := loadState(a.opts.home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[device-runtime]", err)
		return
	}
	device, err := newDeviceRuntime(state, bridge)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[device-runtime]", err)
		return
	}
	a.mu.Lock()
	if a.device != nil {
		a.mu.Unlock()
		return
	}
	a.device = device
	a.deviceDone = make(chan struct{})
	deviceDone := a.deviceDone
	a.mu.Unlock()
	go func() {
		defer close(deviceDone)
		device.Run(ctx)
	}()
}

func (a *desktopApplication) shutdown(context.Context) {
	a.cancelServices()
	select {
	case <-a.done:
	case <-time.After(50 * time.Second):
		fmt.Fprintln(os.Stderr, "[desktop] timed out while stopping managed services")
	}
	a.mu.Lock()
	deviceDone := a.deviceDone
	a.mu.Unlock()
	waitForDeviceRuntime(deviceDone, 20*time.Second)
}

func (a *desktopApplication) cancelServices() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *desktopApplication) requestShutdown() {
	a.cancelServices()
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		wailsruntime.Quit(ctx)
	}
}

func (a *desktopApplication) showStartup() {
	a.assets.ShowStartup()
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		wailsruntime.WindowReload(ctx)
	}
}

func (a *desktopApplication) showUpdateCheck() {
	if a.tracker.current().Deployment.Required {
		a.focusWindow()
		return
	}
	a.tracker.checkingForUpdates()
	signalStartupAction(a.control.checkUpdate)
	a.focusWindow()
}

func (a *desktopApplication) focusWindow() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		wailsruntime.WindowUnminimise(ctx)
		wailsruntime.WindowShow(ctx)
	}
}

func (a *desktopApplication) handleSnapshot(snapshot startupSnapshot) {
	a.mu.Lock()
	previous := a.update
	a.update = snapshot.Update.State
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil || previous == snapshot.Update.State {
		return
	}
	switch snapshot.Update.State {
	case "available", "applying", "error":
		a.assets.ShowStartup()
		wailsruntime.WindowReload(ctx)
	case "none":
		if previous == "available" || previous == "error" {
			a.assets.ShowFrontend()
			wailsruntime.WindowReload(ctx)
		}
	}
}

func stopLegacyLauncher(home string) error {
	if !startupCenterHealthy() {
		return nil
	}
	if err := stopManaged(home); err != nil {
		return fmt.Errorf("stop browser-based Athena before desktop takeover: %w", err)
	}
	deadline := time.Now().Add(50 * time.Second)
	for startupCenterHealthy() && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	if startupCenterHealthy() {
		return fmt.Errorf("browser-based Athena did not stop; inspect %s", filepath.Join(home, "logs", "log"))
	}
	return nil
}

func redirectDesktopLogs(home string) (*os.File, error) {
	logDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(logDir, "log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open desktop log %s: %w", path, err)
	}
	os.Stdout = file
	os.Stderr = file
	return file, nil
}
