package deployment

import (
	"bytes"
	"net/http"
	"runtime/debug"
	"time"

	log "github.com/good-fish-man/logx"
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
				log.Errorf(request.Context(), "[%s] panic recovered method=%s path=%s err=%v\n%s", component, request.Method, request.URL.RequestURI(), recovered, debug.Stack())
				if !writer.wroteHeader {
					http.Error(writer, "internal server error", http.StatusInternalServerError)
				}
			}
			if writer.status >= http.StatusBadRequest {
				log.Errorw(request.Context(), "launcher request failed",
					"component", component,
					"method", request.Method,
					"path", request.URL.RequestURI(),
					"status", writer.status,
					"cost", time.Since(started),
					"response", string(bytes.TrimSpace(writer.body.Bytes())),
				)
			}
		}()
		next.ServeHTTP(writer, request)
	})
}
