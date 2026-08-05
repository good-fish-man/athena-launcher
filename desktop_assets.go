package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// desktopAssetSwitch serves the startup centre until the verified React bundle
// is ready, then switches the same Wails window to the full Athena interface.
type desktopAssetSwitch struct {
	mu           sync.RWMutex
	root         string
	showFrontend bool
	startup      http.Handler
	tracker      *startupTracker
	bridge       http.Handler
}

func (s *desktopAssetSwitch) SetDesktopBridge(bridge http.Handler) {
	s.mu.Lock()
	s.bridge = bridge
	s.mu.Unlock()
}

func newDesktopAssetSwitch(tracker *startupTracker, retry chan<- struct{}, control *startupController) *desktopAssetSwitch {
	return &desktopAssetSwitch{
		startup: startupHandler(tracker, retry, control),
		tracker: tracker,
	}
}

func (s *desktopAssetSwitch) UseFrontend(root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" {
		return fmt.Errorf("desktop frontend path is empty")
	}
	if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
		return fmt.Errorf("desktop frontend is missing index.html in %s: %w", root, err)
	}
	s.mu.Lock()
	s.root = root
	s.showFrontend = true
	s.mu.Unlock()
	return nil
}

func (s *desktopAssetSwitch) ShowStartup() {
	s.mu.Lock()
	s.showFrontend = false
	s.mu.Unlock()
}

func (s *desktopAssetSwitch) ShowFrontend() {
	s.mu.Lock()
	s.showFrontend = s.root != ""
	s.mu.Unlock()
}

func (s *desktopAssetSwitch) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, desktopBridgePrefix) {
		s.mu.RLock()
		bridge := s.bridge
		s.mu.RUnlock()
		if bridge == nil {
			http.NotFound(response, request)
			return
		}
		bridge.ServeHTTP(response, request)
		return
	}
	if request.URL.Path == "/healthz" || strings.HasPrefix(request.URL.Path, "/api/") {
		s.startup.ServeHTTP(response, request)
		return
	}
	s.mu.RLock()
	root := s.root
	showFrontend := s.showFrontend
	s.mu.RUnlock()
	if !showFrontend || root == "" || s.tracker.current().State != "ready" {
		s.startup.ServeHTTP(response, request)
		return
	}
	spaHandler(root).ServeHTTP(response, request)
}
