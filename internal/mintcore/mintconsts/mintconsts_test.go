package mintconsts

import "testing"

func TestOIDCAudience(t *testing.T) {
	if OIDCAudience != "fullsend-mint" {
		t.Fatalf("expected %q, got %q", "fullsend-mint", OIDCAudience)
	}
}
