package deployment

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestErrorLoggerPreservesStatus(t *testing.T) {
	handler := requestErrorLogger("test", http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/action?id=1", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestRequestErrorLoggerRecoversPanic(t *testing.T) {
	handler := requestErrorLogger("test", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("broken handler")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
}
