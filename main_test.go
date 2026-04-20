package main

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestMakeBasePath(t *testing.T) {
	tests := []struct {
		name     string
		req      *http.Request
		expected string
	}{
		{
			name: "HTTP Request",
			req: &http.Request{
				Host: "example.com",
			},
			expected: "http://example.com",
		},
		{
			name: "HTTPS Request",
			req: &http.Request{
				Host: "example.com",
				TLS:  &tls.ConnectionState{},
			},
			expected: "https://example.com",
		},
		{
			name: "HTTP Request with custom port",
			req: &http.Request{
				Host: "example.com:8080",
			},
			expected: "http://example.com:8080",
		},
		{
			name: "HTTPS Request with custom port",
			req: &http.Request{
				Host: "example.com:8443",
				TLS:  &tls.ConnectionState{},
			},
			expected: "https://example.com:8443",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := makeBasePath(tc.req)
			if result != tc.expected {
				t.Errorf("Expected %s, got %s", tc.expected, result)
			}
		})
	}
}
