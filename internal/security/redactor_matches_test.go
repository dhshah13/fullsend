package security

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretRedactor_Matches(t *testing.T) {
	// Matches locates secrets without masking, over the text as given: a
	// runtime value, a prefix token and a structural pattern's whole match,
	// each by its bounds, so a caller can judge text it does not export.
	RegisterRuntimeSecret("runtime-opaque-value-42")
	t.Cleanup(resetRuntimeSecrets)
	token := "ghp_" + strings.Repeat("a", 36)
	text := "x runtime-opaque-value-42 " + token + " API_KEY=s3cr3tvalue99 end"

	got := map[string]string{}
	for _, m := range NewSecretRedactor().Matches(text) {
		got[m.Name] = text[m.Start:m.End]
	}
	assert.Equal(t, map[string]string{
		"runtime_secret": "runtime-opaque-value-42",
		"github_pat":     token,
		"env_assignment": " API_KEY=s3cr3tvalue99",
	}, got)
	assert.Empty(t, NewSecretRedactor().Matches("nothing here"))
}
