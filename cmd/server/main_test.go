package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecretPath(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	handler := secretPath(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	tests := []struct {
		path string
		want int
	}{
		{"/mcp/" + secret, http.StatusTeapot},
		{"/mcp/" + secret + "/", http.StatusTeapot},
		{"/mcp/" + secret[:31], http.StatusNotFound},
		{"/mcp/" + secret + "x", http.StatusNotFound},
		{"/mcp/" + secret + "/other", http.StatusNotFound},
		{"/mcp", http.StatusNotFound},
		{"/", http.StatusNotFound},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tt.path, nil))
		if rec.Code != tt.want {
			t.Errorf("%s: got %d, want %d", tt.path, rec.Code, tt.want)
		}
	}
}
