package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// OpenAI on pi (#6689, ADR 0092): the credential is a short-lived access
// token the runner obtains itself — through OpenAI Workload Identity
// Federation from the job's GitHub OIDC token, or from OPENAI_API_KEY in
// the runner environment for local runs — and hands to a run-scoped
// OpenShell provider. The sandbox only ever sees the gateway's placeholder.

// openAIProviderType is the provider profile id that selects this path
// (internal/scaffold/fullsend-repo/profiles/fullsend-openai.yaml).
const openAIProviderType = "fullsend-openai"

// openAIDefaultCredentialKey is used when the provider definition declares
// no credential keys at all.
const openAIDefaultCredentialKey = "OPENAI_API_KEY"

// Runner environment variables for the WIF path. All three are non-secret
// identifiers; they are also in oidcDenyKeys so they never reach the
// sandbox or user scripts.
const (
	openAIAudienceEnv           = "FULLSEND_OPENAI_AUDIENCE"
	openAIIdentityProviderIDEnv = "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID"
	openAIServiceAccountIDEnv   = "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID"
	openAIStaticKeyEnv          = "OPENAI_API_KEY"
)

// openAIExchange is the WIF exchange; tests substitute it.
var openAIExchange = openaiwif.Exchange

// openAIStaticKeyLifetime bounds a provider instance backed by a static
// OPENAI_API_KEY. The key itself does not expire, but the run-scoped
// provider must: if the runner dies before the deferred delete, the gateway
// fails placeholder resolution closed after this long instead of serving the
// key indefinitely. Longer than any fleet harness timeout.
const openAIStaticKeyLifetime = time.Hour

// openAICredential is a resolved OpenAI credential and where it came from.
type openAICredential struct {
	value string
	// expiresAt is zero for a static key until ensureOpenAIProvider bounds it.
	expiresAt time.Time
	// source is "wif" or "static"; detail is a printable, secret-free
	// description for the run log.
	source string
	detail string
}

// resolveOpenAICredential picks the credential source for a fullsend-openai
// provider, in order:
//
//  1. WIF — all three FULLSEND_OPENAI_* ids present: exchange the job's
//     GitHub OIDC token (ACTIONS_ID_TOKEN_REQUEST_URL/_TOKEN) for an OpenAI
//     access token.
//  2. Static — OPENAI_API_KEY present in the runner environment (local runs).
//  3. Neither — an error naming the variables, before any gateway work.
//
// A partially configured WIF trio is an error rather than a silent fall
// through to a static key, so a typo in one variable cannot switch the run
// onto a different credential.
func resolveOpenAICredential(ctx context.Context, getenv func(string) string) (openAICredential, error) {
	audience := strings.TrimSpace(getenv(openAIAudienceEnv))
	identityProviderID := strings.TrimSpace(getenv(openAIIdentityProviderIDEnv))
	serviceAccountID := strings.TrimSpace(getenv(openAIServiceAccountIDEnv))

	if audience != "" || identityProviderID != "" || serviceAccountID != "" {
		var missing []string
		for _, kv := range []struct{ k, v string }{
			{openAIAudienceEnv, audience},
			{openAIIdentityProviderIDEnv, identityProviderID},
			{openAIServiceAccountIDEnv, serviceAccountID},
		} {
			if kv.v == "" {
				missing = append(missing, kv.k)
			}
		}
		if len(missing) > 0 {
			return openAICredential{}, fmt.Errorf("OpenAI WIF is partially configured: missing %s", strings.Join(missing, ", "))
		}
		oidcURL := getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
		oidcToken := getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
		if oidcURL == "" || oidcToken == "" {
			return openAICredential{}, fmt.Errorf("OpenAI WIF is configured but the job has no GitHub OIDC endpoint (ACTIONS_ID_TOKEN_REQUEST_URL/ACTIONS_ID_TOKEN_REQUEST_TOKEN unset): the exchange is GitHub Actions only — grant the workflow `permissions: id-token: write`; on GitLab CI or a local run use OPENAI_API_KEY instead")
		}
		tok, err := openAIExchange(ctx, openaiwif.Config{
			Audience:           audience,
			IdentityProviderID: identityProviderID,
			ServiceAccountID:   serviceAccountID,
			OIDCRequestURL:     oidcURL,
			OIDCRequestToken:   oidcToken,
		})
		if err != nil {
			return openAICredential{}, fmt.Errorf("OpenAI WIF exchange failed: %w", err)
		}
		return openAICredential{
			value:     tok.Value,
			expiresAt: tok.ExpiresAt,
			source:    "wif",
			detail:    fmt.Sprintf("WIF: identity provider %s, service account %s, audience %s", identityProviderID, serviceAccountID, audience),
		}, nil
	}

	if key := getenv(openAIStaticKeyEnv); key != "" {
		return openAICredential{
			value:  key,
			source: "static",
			detail: openAIStaticKeyEnv + " from the runner environment",
		}, nil
	}

	return openAICredential{}, fmt.Errorf("no OpenAI credential: set %s, %s and %s for Workload Identity Federation (the job needs `permissions: id-token: write`), or %s in the runner environment for a local run",
		openAIAudienceEnv, openAIIdentityProviderIDEnv, openAIServiceAccountIDEnv, openAIStaticKeyEnv)
}

// runScopedProviderName derives the provider instance name for this run
// from the harness provider name and the sandbox name's hash suffix, e.g.
// "openai-3f9c2a7b1d0e". Two concurrent runs on one gateway therefore never
// share (or overwrite) a provider instance.
func runScopedProviderName(base, sandboxName string) string {
	const suffixLen = 12
	suffix := sandboxName
	if i := strings.LastIndex(sandboxName, "-"); i >= 0 {
		suffix = sandboxName[i+1:]
	}
	if len(suffix) > suffixLen {
		suffix = suffix[:suffixLen]
	}
	return base + "-" + suffix
}

// applyRunScopedProviderNames substitutes run-scoped instance names into
// the list of providers the sandbox attaches, keeping order.
func applyRunScopedProviderNames(names []string, scoped map[string]string) []string {
	if len(scoped) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if s, ok := scoped[n]; ok {
			out = append(out, s)
			continue
		}
		out = append(out, n)
	}
	return out
}

// openAICredentialKeys returns the credential keys a provider definition
// declares, or the default key when it declares none.
func openAICredentialKeys(pd harness.ProviderDef) []string {
	keys := make([]string, 0, len(pd.Credentials))
	for k := range pd.Credentials {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		keys = append(keys, openAIDefaultCredentialKey)
	}
	sort.Strings(keys)
	return keys
}

// ensureOpenAIProvider resolves the credential, registers it for redaction,
// and creates the run-scoped provider instance for a fullsend-openai
// harness provider. It returns the instance name the sandbox must attach
// and the credential keys it carries (for cleanupRunScopedProvider).
//
// The credential value is the same for every key the definition declares
// (normally just OPENAI_API_KEY); literal or ${VAR} values in the
// definition are ignored on purpose — the value is supplied in process.
func ensureOpenAIProvider(ctx context.Context, pd harness.ProviderDef, sandboxName string, printer *ui.Printer) (string, []string, error) {
	name := runScopedProviderName(pd.Name, sandboxName)
	printer.StepStart("Resolving OpenAI credential for provider: " + pd.Name)
	cred, err := resolveOpenAICredential(ctx, os.Getenv)
	if err != nil {
		printer.StepFail("OpenAI credential unavailable for provider " + pd.Name)
		return "", nil, fmt.Errorf("provider %q: %w", pd.Name, err)
	}
	// Two redaction layers: the exact value in the process-wide redactor
	// (the token is opaque — no prefix pattern can be trusted), and the
	// Actions log mask so nothing this process prints can echo it.
	security.RegisterRuntimeSecret(cred.value)
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Fprintf(os.Stderr, "::add-mask::%s\n", cred.value)
	}
	printer.StepDone("OpenAI credential ready (" + cred.detail + ")")
	if cred.source == "static" {
		if os.Getenv("GITHUB_ACTIONS") == "true" || os.Getenv("CI") != "" {
			printer.StepWarn("OpenAI credential is a static OPENAI_API_KEY in CI; prefer Workload Identity Federation (FULLSEND_OPENAI_AUDIENCE, FULLSEND_OPENAI_IDENTITY_PROVIDER_ID, FULLSEND_OPENAI_SERVICE_ACCOUNT_ID) so no long-lived key is stored")
		}
		cred.expiresAt = time.Now().Add(openAIStaticKeyLifetime)
	}

	keys := openAICredentialKeys(pd)
	creds := make(map[string]string, len(keys))
	for _, k := range keys {
		creds[k] = cred.value
	}

	start := time.Now()
	printer.StepStart("Ensuring run-scoped provider: " + name)
	if err := sandbox.EnsureProviderLiteral(ctx, name, pd.Type, creds); err != nil {
		printer.StepFail("Failed to create run-scoped provider " + name)
		return "", nil, fmt.Errorf("ensuring provider %q: %w", name, err)
	}
	for _, k := range keys {
		if err := sandbox.SetProviderCredentialExpiry(ctx, name, k, cred.expiresAt); err != nil {
			// The caller only registers the deferred delete once this
			// function succeeds; a provider without an expiry must not be
			// left behind.
			printer.StepFail("Failed to set credential expiry on " + name)
			if delErr := sandbox.DeleteProvider(name); delErr != nil {
				printer.StepWarn("Run-scoped provider cleanup failed: " + delErr.Error())
			}
			return "", nil, err
		}
	}
	printer.StepDone(fmt.Sprintf("Provider ready: %s (%s, expires in %s, %.1fs)", name, cred.source, time.Until(cred.expiresAt).Round(time.Minute), time.Since(start).Seconds()))
	return name, keys, nil
}

// cleanupRunScopedProvider removes a run-scoped provider at the end of the
// run. OpenShell refuses to delete a provider that a sandbox still
// references (FAILED_PRECONDITION), which is exactly the --keep-sandbox
// case, so when the delete fails the credential is expired in place
// instead: the gateway then fails placeholder resolution closed for the
// kept sandbox while the record stays deletable later. Uses a background
// context because the run's context is usually cancelled by the time the
// deferred cleanup runs.
func cleanupRunScopedProvider(name string, keys []string, printer *ui.Printer) {
	delErr := sandbox.DeleteProvider(name)
	if delErr == nil {
		printer.StepDone("Run-scoped provider deleted: " + name)
		return
	}
	now := time.Now()
	var expireErr error
	for _, k := range keys {
		if err := sandbox.SetProviderCredentialExpiry(context.Background(), name, k, now); err != nil {
			expireErr = err
			break
		}
	}
	if expireErr != nil {
		printer.StepWarn(fmt.Sprintf("Run-scoped provider %s could not be deleted (%v) or expired in place (%v); expiry set at creation is the backstop", name, delErr, expireErr))
		return
	}
	printer.StepWarn(fmt.Sprintf("Run-scoped provider %s expired in place instead of deleted (a kept sandbox still references it: %v)", name, delErr))
}
