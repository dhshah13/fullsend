//go:build !js

package mintcore

import (
	"testing"
)

func TestMintEnv_ReadsOsGetenv(t *testing.T) {
	t.Setenv("TEST_MINTENV_KEY", "test-value")
	got := mintEnv("TEST_MINTENV_KEY")
	if got != "test-value" {
		t.Fatalf("mintEnv(TEST_MINTENV_KEY) = %q, want %q", got, "test-value")
	}
}

func TestMintEnv_ReturnsEmptyForUnset(t *testing.T) {
	t.Setenv("TEST_MINTENV_UNSET", "")
	got := mintEnv("TEST_MINTENV_UNSET")
	if got != "" {
		t.Fatalf("mintEnv(TEST_MINTENV_UNSET) = %q, want empty", got)
	}
}
