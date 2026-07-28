package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type frontendServer struct {
	server  *http.Server
	address string
}

func startFrontendServer(manifest *Manifest, root string) (*frontendServer, error) {
	if manifest.Frontend == nil {
		return nil, nil
	}
	indexPath := filepath.Join(root, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		return nil, fmt.Errorf("frontend package is missing %s: %w", indexPath, err)
	}
	address := strings.TrimSpace(manifest.Frontend.ListenAddr)
	if address == "" {
		address = fmt.Sprintf("127.0.0.1:%d", defaultFrontendPort)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("frontend listen %s: %w", address, err)
	}
	server := &http.Server{Addr: address, Handler: requestErrorLogger("frontend", spaHandler(root)), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "[frontend]", err)
		}
	}()
	fmt.Printf("[frontend] serving %s at http://%s\n", root, address)
	return &frontendServer{server: server, address: address}, nil
}

func (s *frontendServer) Stop(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func spaHandler(root string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		clean := strings.TrimPrefix(filepath.Clean(filepath.FromSlash(request.URL.Path)), string(filepath.Separator))
		for _, segment := range strings.Split(clean, string(filepath.Separator)) {
			if strings.HasPrefix(segment, ".") {
				http.NotFound(response, request)
				return
			}
		}
		candidate := filepath.Join(root, clean)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			http.ServeFile(response, request, candidate)
			return
		}
		http.ServeFile(response, request, filepath.Join(root, "index.html"))
	})
}
