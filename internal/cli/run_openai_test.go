package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func openAITestEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func stubOpenAIExchange(t *testing.T, fn func(ctx context.Context, cfg openaiwif.Config) (*openaiwif.Token, error)) {
	t.Helper()
	orig := openAIExchange
	openAIExchange = fn
	t.Cleanup(func() { openAIExchange = orig })
}

func TestResolveOpenAICredential_WIF(t *testing.T) {
	var got openaiwif.Config
	expires := time.Now().Add(59 * time.Minute)
	stubOpenAIExchange(t, func(_ context.Context, cfg openaiwif.Config) (*openaiwif.Token, error) {
		got = cfg
		return &openaiwif.Token{Value: "opaque-token-$with$dollars", ExpiresAt: expires}, nil
	})

	cred, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"FULLSEND_OPENAI_AUDIENCE":             " fullsend://acme ",
		"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID": "idp-1",
		"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID":   "sa-1",
		"ACTIONS_ID_TOKEN_REQUEST_URL":         "https://oidc.example/token?api-version=2",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN":       "runner-token",
		// A static key alongside a full WIF trio must not win.
		"OPENAI_API_KEY": "sk-static",
	}))
	require.NoError(t, err)
	assert.Equal(t, "opaque-token-$with$dollars", cred.value)
	assert.Equal(t, expires, cred.expiresAt)
	assert.Equal(t, "wif", cred.source)
	assert.Contains(t, cred.detail, "idp-1")
	assert.Contains(t, cred.detail, "sa-1")
	assert.NotContains(t, cred.detail, "opaque-token", "detail must never carry the token")

	assert.Equal(t, "fullsend://acme", got.Audience, "audience is trimmed")
	assert.Equal(t, "idp-1", got.IdentityProviderID)
	assert.Equal(t, "sa-1", got.ServiceAccountID)
	assert.Equal(t, "https://oidc.example/token?api-version=2", got.OIDCRequestURL)
	assert.Equal(t, "runner-token", got.OIDCRequestToken)
}

func TestResolveOpenAICredential_WIFExchangeError(t *testing.T) {
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return nil, errors.New("token endpoint returned 401")
	})
	_, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"FULLSEND_OPENAI_AUDIENCE":             "aud",
		"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID": "idp",
		"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID":   "sa",
		"ACTIONS_ID_TOKEN_REQUEST_URL":         "https://oidc.example/token",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN":       "runner-token",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OpenAI WIF exchange failed")
	assert.Contains(t, err.Error(), "401")
}

func TestResolveOpenAICredential_PartialWIFIsAnError(t *testing.T) {
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		t.Fatal("exchange must not be attempted with a partial configuration")
		return nil, nil
	})
	_, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"FULLSEND_OPENAI_AUDIENCE": "aud",
		"OPENAI_API_KEY":           "sk-static",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "partially configured")
	assert.Contains(t, err.Error(), "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID")
	assert.Contains(t, err.Error(), "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID")
	assert.NotContains(t, err.Error(), "FULLSEND_OPENAI_AUDIENCE, ", "only the missing variables are listed")
}

func TestResolveOpenAICredential_WIFWithoutOIDCEndpoint(t *testing.T) {
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		t.Fatal("exchange must not be attempted without the OIDC endpoint")
		return nil, nil
	})
	_, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"FULLSEND_OPENAI_AUDIENCE":             "aud",
		"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID": "idp",
		"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID":   "sa",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ACTIONS_ID_TOKEN_REQUEST_URL")
	assert.Contains(t, err.Error(), "id-token: write")
}

func TestResolveOpenAICredential_Static(t *testing.T) {
	cred, err := resolveOpenAICredential(context.Background(), openAITestEnv(map[string]string{
		"OPENAI_API_KEY": "sk-local-dev",
	}))
	require.NoError(t, err)
	assert.Equal(t, "sk-local-dev", cred.value)
	assert.True(t, cred.expiresAt.IsZero(), "a static key has no expiry")
	assert.Equal(t, "static", cred.source)
	assert.NotContains(t, cred.detail, "sk-local-dev")
}

func TestResolveOpenAICredential_NothingConfigured(t *testing.T) {
	_, err := resolveOpenAICredential(context.Background(), openAITestEnv(nil))
	require.Error(t, err)
	for _, v := range []string{
		"FULLSEND_OPENAI_AUDIENCE",
		"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID",
		"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID",
		"OPENAI_API_KEY",
	} {
		assert.Contains(t, err.Error(), v)
	}
}

func TestRunScopedProviderName(t *testing.T) {
	assert.Equal(t, "openai-0123456789ab", runScopedProviderName("openai", "fs-tri-0123456789abcdef0123"))
	assert.Equal(t, "openai-abc", runScopedProviderName("openai", "abc"), "short names are used whole")
	a := runScopedProviderName("openai", generateSandboxName("triage"))
	b := runScopedProviderName("openai", generateSandboxName("triage"))
	assert.NotEqual(t, a, b, "two runs get distinct instances")
	assert.True(t, strings.HasPrefix(a, "openai-"))
}

func TestApplyRunScopedProviderNames(t *testing.T) {
	names := []string{"github", "openai", "vertex-ai"}
	assert.Equal(t, names, applyRunScopedProviderNames(names, nil), "no mapping is a no-op")
	got := applyRunScopedProviderNames(names, map[string]string{"openai": "openai-abc123"})
	assert.Equal(t, []string{"github", "openai-abc123", "vertex-ai"}, got, "order is preserved")
	assert.Equal(t, []string{"github", "openai", "vertex-ai"}, names, "input is not mutated")
}

// fakeOpenshellRecorder installs an openshell stub on PATH that appends every
// invocation's arguments to argsLog and the OPENAI_API_KEY it saw in its
// environment to envLog.
//
// failOn, when non-empty, makes any invocation whose arguments contain that
// substring exit 1 (after logging), so a single step can be made to fail.
func fakeOpenshellRecorder(t *testing.T, failOn ...string) (argsLog, envLog string) {
	t.Helper()
	binDir := t.TempDir()
	argsLog = filepath.Join(binDir, "args.log")
	envLog = filepath.Join(binDir, "env.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuoteForTest(argsLog) + "\n" +
		"printf '%s\\n' \"${OPENAI_API_KEY-<unset>}\" >> " + shellQuoteForTest(envLog) + "\n"
	for _, f := range failOn {
		script += "case \"$*\" in *" + shellQuoteForTest(f) + "*) echo 'stub: forced failure' >&2; exit 1 ;; esac\n"
	}
	script += "exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsLog, envLog
}

func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func TestEnsureOpenAIProvider_WIF(t *testing.T) {
	argsLog, envLog := fakeOpenshellRecorder(t)
	expires := time.Date(2026, 8, 27, 20, 0, 0, 0, time.UTC)
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return &openaiwif.Token{Value: "tok-$literal$-value-9f8e7d", ExpiresAt: expires}, nil
	})
	t.Setenv("FULLSEND_OPENAI_AUDIENCE", "aud")
	t.Setenv("FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "idp")
	t.Setenv("FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "sa")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-token")
	t.Setenv("GITHUB_ACTIONS", "")

	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType, Credentials: map[string]string{"OPENAI_API_KEY": ""}}
	name, keys, err := ensureOpenAIProvider(context.Background(), pd, "fs-tri-0123456789abcdef", ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, "openai-0123456789ab", name)
	assert.Equal(t, []string{"OPENAI_API_KEY"}, keys)

	args, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	require.Len(t, lines, 2, "create, then expiry: %q", lines)
	assert.Equal(t, "provider create --name openai-0123456789ab --type fullsend-openai --credential OPENAI_API_KEY", lines[0], "bare-key form: the value is not on the command line")
	assert.Equal(t, "provider update openai-0123456789ab --credential-expires-at OPENAI_API_KEY=2026-08-27T20:00:00Z", lines[1])

	env, err := os.ReadFile(envLog)
	require.NoError(t, err)
	envLines := strings.Split(strings.TrimSpace(string(env)), "\n")
	assert.Equal(t, "tok-$literal$-value-9f8e7d", envLines[0], "the value reaches the child verbatim, `$` intact")

	res := security.NewSecretRedactor().Scan("log line with tok-$literal$-value-9f8e7d inside")
	assert.False(t, res.Safe)
	assert.NotContains(t, res.Sanitized, "9f8e7d", "the token is registered for exact-value redaction")
}

func TestEnsureOpenAIProvider_StaticGetsBoundedExpiry(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t)
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", "sk-local-static-key")
	t.Setenv("GITHUB_ACTIONS", "")

	// No credential keys declared: the default key is used.
	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType}
	name, keys, err := ensureOpenAIProvider(context.Background(), pd, "fs-cod-feedface", ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, "openai-feedface", name)
	assert.Equal(t, []string{"OPENAI_API_KEY"}, keys, "the default key when the definition declares none")

	args, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	require.Len(t, lines, 2, "create, then a bounded expiry even for a static key: %q", lines)
	assert.Equal(t, "provider create --name openai-feedface --type fullsend-openai --credential OPENAI_API_KEY", lines[0])
	require.Regexp(t, `^provider update openai-feedface --credential-expires-at OPENAI_API_KEY=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`, lines[1])
	stamp := strings.TrimPrefix(strings.Fields(lines[1])[4], "OPENAI_API_KEY=")
	at, err := time.Parse(time.RFC3339, stamp)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(openAIStaticKeyLifetime), at, 2*time.Minute, "static keys are bounded to the configured lifetime")
}

func TestEnsureOpenAIProvider_ExpiryFailureDeletesProvider(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t, "--credential-expires-at")
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", "sk-local-static-key")
	t.Setenv("GITHUB_ACTIONS", "")

	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType}
	_, _, err := ensureOpenAIProvider(context.Background(), pd, "fs-cod-feedface", ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential expiry")

	args, readErr := os.ReadFile(argsLog)
	require.NoError(t, readErr)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	require.Len(t, lines, 3, "create, failed expiry, delete: %q", lines)
	assert.True(t, strings.HasPrefix(lines[1], "provider update openai-feedface --credential-expires-at"))
	assert.Equal(t, "provider delete openai-feedface", lines[2], "a provider whose expiry could not be set is not left behind")
}

func TestEnsureOpenAIProvider_NoCredentialFailsBeforeOpenshell(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t)
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType}
	_, _, err := ensureOpenAIProvider(context.Background(), pd, "fs-cod-feedface", ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `provider "openai"`)
	assert.Contains(t, err.Error(), "no OpenAI credential")
	_, statErr := os.Stat(argsLog)
	assert.True(t, os.IsNotExist(statErr), "openshell must not be invoked without a credential")
}

// recordingProvidersStub installs an openshell stub that behaves like
// testdata/providers-stub (passes the gateway check and every
// provider/profile/sandbox command) and additionally appends each
// invocation's arguments to the returned log file.
func recordingProvidersStub(t *testing.T) string {
	t.Helper()
	neutralizeAgentsRepoFallback(t)
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuoteForTest(logPath) + "\n" +
		"case \"$1 $2\" in\n" +
		"  'gateway list') echo default-gateway; exit 0 ;;\n" +
		"  'settings '*) exit 0 ;;\n" +
		"  'provider profile'|'provider create'|'provider update'|'provider delete') exit 0 ;;\n" +
		"  'sandbox create'|'sandbox delete'|'sandbox ready') exit 0 ;;\n" +
		"  'sandbox get') echo 'Status: Ready'; exit 0 ;;\n" +
		// The first in-sandbox command fails so the run stops right after
		// sandbox creation and the deferred cleanup runs.
		"  'sandbox exec') echo 'stub: no sandbox' >&2; exit 1 ;;\n" +
		"esac\n" +
		"echo \"openshell stub: unhandled: $*\" >&2; exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// writeOpenAIFullsendDir lays out a fullsend dir whose code harness declares
// the openai provider — by bare name, or by file path the way the
// behaviour scenarios do — with the scaffold's provider and profile files.
func writeOpenAIFullsendDir(t *testing.T, pathForm bool) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"harness", "agents", "providers", "profiles"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"), []byte("You are a coding agent."), 0o644))
	harnessYAML := "agent: agents/code.md\nrole: test\nproviders:\n  - openai\n"
	if pathForm {
		harnessYAML = "agent: agents/code.md\nrole: test\nprofiles:\n  - profiles/fullsend-openai.yaml\nproviders:\n  - providers/openai.yaml\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(harnessYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "openai.yaml"),
		[]byte("name: openai\ntype: fullsend-openai\ncredentials:\n  OPENAI_API_KEY: \"\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "fullsend-openai.yaml"),
		[]byte("id: fullsend-openai\ndisplay_name: Fullsend OpenAI\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("agents:\n  - harness/code.yaml\n"), 0o644))
	return dir
}

func TestRunAgent_OpenAIProviderIsRunScopedAndDeleted(t *testing.T) {
	cases := []struct {
		name     string
		keep     bool
		pathForm bool
	}{
		{"keepSandbox=false", false, false},
		{"keepSandbox=true", true, false},
		{"path-form provider entry", false, true},
	}
	for _, tc := range cases {
		keep := tc.keep
		t.Run(tc.name, func(t *testing.T) {
			logPath := recordingProvidersStub(t)
			for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "GITHUB_ACTIONS"} {
				t.Setenv(k, "")
			}
			t.Setenv("OPENAI_API_KEY", "sk-local-static-key-for-test")
			dir := writeOpenAIFullsendDir(t, tc.pathForm)

			rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
			err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags, statusOpts{}, ui.New(io.Discard), keep, runOverrideFlags{})
			// The run fails after sandbox creation (the stub cannot bootstrap
			// an agent), but the provider block must have completed.
			require.Error(t, err)
			t.Logf("runAgent returned (expected to fail after the provider block): %v", err)
			assert.NotContains(t, err.Error(), "ensuring provider")
			assert.NotContains(t, err.Error(), "no OpenAI credential")

			data, readErr := os.ReadFile(logPath)
			require.NoError(t, readErr)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")

			var createLine, sandboxLine, deleteLine, sandboxDelete string
			for _, l := range lines {
				switch {
				case strings.HasPrefix(l, "provider create "):
					createLine = l
				case strings.HasPrefix(l, "sandbox create "):
					sandboxLine = l
				case strings.HasPrefix(l, "provider delete "):
					deleteLine = l
				case strings.HasPrefix(l, "sandbox delete "):
					sandboxDelete = l
				}
			}
			require.NotEmpty(t, createLine, "provider created: %q", lines)
			assert.Regexp(t, `^provider create --name openai-[0-9a-f]{12} --type fullsend-openai --credential OPENAI_API_KEY$`, createLine,
				"run-scoped name, bare-key credential, no value on the command line")
			assert.NotContains(t, strings.Join(lines, "\n"), "sk-local-static-key-for-test", "the value never reaches a command line")
			scoped := strings.Fields(createLine)[3]

			require.NotEmpty(t, sandboxLine, "sandbox created: %q", lines)
			assert.Contains(t, sandboxLine, "--provider "+scoped, "the sandbox attaches the run-scoped instance")
			assert.NotRegexp(t, `--provider openai( |$)`, sandboxLine, "the bare harness name is not attached")

			assert.Equal(t, "provider delete "+scoped, deleteLine, "the run-scoped provider is deleted at run end")
			if keep {
				assert.Empty(t, sandboxDelete, "--keep-sandbox keeps the sandbox but not the live-token provider")
			} else {
				assert.NotEmpty(t, sandboxDelete)
			}
		})
	}
}

func TestReservedSandboxKeys_IncludesOpenAIKey(t *testing.T) {
	assert.True(t, reservedSandboxKeys["OPENAI_API_KEY"], "env.sandbox must not be able to shadow the provider placeholder")
}

func TestCleanupRunScopedProvider_ExpiresInPlaceWhenDeleteFails(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t, "provider delete")
	cleanupRunScopedProvider("openai-feedface", []string{"OPENAI_API_KEY"}, ui.New(io.Discard))

	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 2, "delete attempted, then expired in place: %q", lines)
	assert.Equal(t, "provider delete openai-feedface", lines[0])
	require.Regexp(t, `^provider update openai-feedface --credential-expires-at OPENAI_API_KEY=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`, lines[1])
	stamp := strings.TrimPrefix(strings.Fields(lines[1])[4], "OPENAI_API_KEY=")
	at, err := time.Parse(time.RFC3339, stamp)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), at, time.Minute, "expired now, so a kept sandbox fails closed")
}

func TestCleanupRunScopedProvider_Deleted(t *testing.T) {
	argsLog, _ := fakeOpenshellRecorder(t)
	cleanupRunScopedProvider("openai-feedface", []string{"OPENAI_API_KEY"}, ui.New(io.Discard))
	assert.Equal(t, []string{"provider delete openai-feedface"}, readArgLines(t, argsLog), "no expiry call when the delete succeeds")
}

func readArgLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}
