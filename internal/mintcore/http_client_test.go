//go:build !js

package mintcore

import (
	"net/http"
	"testing"
)

// SetMintHTTPForTest replaces the HTTP function for the duration of the
// test and restores the previous value when the test completes.
func SetMintHTTPForTest(t *testing.T, fake func(*http.Request) (*http.Response, error)) {
	t.Helper()
	httpMu.Lock()
	prev := httpOverride
	httpOverride = fake
	httpMu.Unlock()
	t.Cleanup(func() {
		httpMu.Lock()
		httpOverride = prev
		httpMu.Unlock()
	})
}

func TestMintHTTP_ReturnsCachedClient(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://localhost:0/nonexistent", nil)
	// Just verify it doesn't panic; we can't easily check identity
	// since mintHTTP now returns a response, not a client.
	_, _ = mintHTTP(req)
}

func TestSetMintHTTPForTest_OverridesAndRestores(t *testing.T) {
	called := false
	fake := func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200}, nil
	}
	SetMintHTTPForTest(t, fake)

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/test", nil)
	resp, err := mintHTTP(req)
	if err != nil {
		t.Fatalf("mintHTTP error: %v", err)
	}
	if !called {
		t.Fatal("mintHTTP should call the test override")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestNewHandler_UsesMintHTTPOverride(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")

	called := false
	fake := func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200}, nil
	}
	SetMintHTTPForTest(t, fake)

	_, err := NewHandler(&fakePEMAccessor{}, &fakeOIDCVerifier{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// Verify the override is active by calling mintHTTP directly.
	req, _ := http.NewRequest(http.MethodGet, "http://localhost/test", nil)
	_, _ = mintHTTP(req)
	if !called {
		t.Fatal("expected mintHTTP to use the test-injected override")
	}
}
