package gobluetti

import (
	"testing"
	"time"
)

// TestDefaultAPITimeout pins the default the documentation promises: a gateway
// that accepts a request and never answers holds a call for 15 seconds, while
// SSO keeps its own default.
func TestDefaultAPITimeout(t *testing.T) {
	if got := apiHTTPClient.Timeout; got != 15*time.Second {
		t.Errorf("default API client timeout = %v, want 15s", got)
	}
	if apiHTTPClient == defaultHTTPClient {
		t.Error("API and SSO share one default client, want separate defaults")
	}
}
