package main

import (
	"bytes"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"
)

type launcherResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (w *launcherResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *launcherResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if remaining := 4*1024 - w.body.Len(); remaining > 0 {
		if len(data) < remaining {
			remaining = len(data)
		}
		_, _ = w.body.Write(data[:remaining])
	}
	return w.ResponseWriter.Write(data)
}

func requestErrorLogger(component string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		writer := &launcherResponseWriter{ResponseWriter: response, status: http.StatusOK}
		defer func() {
			if recovered := recover(); recovered != nil {
				fmt.Printf("[%s] ERROR method=%s path=%s panic=%v\n%s", component, request.Method, request.URL.RequestURI(), recovered, debug.Stack())
				if !writer.wroteHeader {
					http.Error(writer, "internal server error", http.StatusInternalServerError)
				}
			}
			if writer.status >= http.StatusBadRequest {
				fmt.Printf("[%s] request failed method=%s path=%s status=%d cost=%s response=%q\n", component, request.Method, request.URL.RequestURI(), writer.status, time.Since(started), bytes.TrimSpace(writer.body.Bytes()))
			}
		}()
		next.ServeHTTP(writer, request)
	})
}
