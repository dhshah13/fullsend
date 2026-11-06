package forge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatSignOffTrailer(t *testing.T) {
	got, err := FormatSignOffTrailer("Alice Smith", "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "Signed-off-by: Alice Smith <alice@example.com>", got)
}

func TestFormatSignOffTrailer_StripsNewlines(t *testing.T) {
	got, err := FormatSignOffTrailer("Evil\nUser", "evil@example.com\r")
	require.NoError(t, err)
	assert.Equal(t, "Signed-off-by: EvilUser <evil@example.com>", got)
}

func TestFormatSignOffTrailer_StripsAngleBracketsFromName(t *testing.T) {
	got, err := FormatSignOffTrailer("Evil>User", "evil@example.com")
	require.NoError(t, err)
	assert.Equal(t, "Signed-off-by: EvilUser <evil@example.com>", got)

	got, err = FormatSignOffTrailer("User <injected>", "user@example.com")
	require.NoError(t, err)
	assert.Equal(t, "Signed-off-by: User injected <user@example.com>", got)
}

func TestFormatSignOffTrailer_StripsAngleBracketsFromEmail(t *testing.T) {
	got, err := FormatSignOffTrailer("User", "<user@example.com>")
	require.NoError(t, err)
	assert.Equal(t, "Signed-off-by: User <user@example.com>", got)
}

func TestFormatSignOffTrailer_ErrorsOnEmptyNameAfterSanitization(t *testing.T) {
	_, err := FormatSignOffTrailer("\n\r", "user@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-empty name and email after sanitization")
}

func TestFormatSignOffTrailer_ErrorsOnEmptyEmailAfterSanitization(t *testing.T) {
	_, err := FormatSignOffTrailer("User", "<>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-empty name and email after sanitization")
}

func TestFormatSignOffTrailer_ErrorsOnBothEmptyAfterSanitization(t *testing.T) {
	_, err := FormatSignOffTrailer("<>", "\n\r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-empty name and email after sanitization")
}

func TestUserIdentity_SignOffTrailer(t *testing.T) {
	id := &UserIdentity{Name: "Test User", Email: "test@example.com"}
	got, err := id.SignOffTrailer()
	require.NoError(t, err)
	assert.Equal(t, "Signed-off-by: Test User <test@example.com>", got)
}
