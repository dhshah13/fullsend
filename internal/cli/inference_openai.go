package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	// openAIRequestSchemaVersion is the versioned schema for the request
	// JSON, so a future `fullsend inference openai apply request.json`
	// can submit it unchanged.
	openAIRequestSchemaVersion = "1"

	// defaultOpenAIAudiencePrefix is the default audience convention.
	defaultOpenAIAudiencePrefix = "fullsend://"

	// githubOIDCIssuer is the fixed OIDC issuer for GitHub Actions.
	githubOIDCIssuer = "https://token.actions.githubusercontent.com"

	// openAIDefaultPermission is the only permission an agent run needs.
	openAIDefaultPermission = "api.model.request"

	// openAIDefaultRef is the ref assertion for the mapping.
	openAIDefaultRef = "refs/heads/main"
)

// --- request command ---

// openAIRequestDoc is the top-level JSON schema for the request document.
type openAIRequestDoc struct {
	Version  string                 `json:"version"`
	Provider openAIRequestProvider  `json:"provider"`
	Mappings []openAIRequestMapping `json:"mappings"`
	Reply    openAIRequestReply     `json:"reply"`
}

type openAIRequestProvider struct {
	Issuer       string `json:"issuer"`
	Audience     string `json:"audience"`
	UploadedJWKS bool   `json:"uploaded_jwks"`
}

type openAIRequestMapping struct {
	Repository string                  `json:"repository"`
	Assertions openAIRequestAssertions `json:"assertions"`
	Target     openAIRequestTarget     `json:"target"`
}

type openAIRequestAssertions struct {
	Iss        string `json:"iss"`
	Aud        string `json:"aud"`
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
}

type openAIRequestTarget struct {
	Project        string   `json:"project"`
	ServiceAccount string   `json:"service_account"`
	Permissions    []string `json:"permissions"`
}

type openAIRequestReply struct {
	IdentityProviderID string            `json:"identity_provider_id"`
	Audience           string            `json:"audience"`
	ServiceAccountIDs  map[string]string `json:"service_account_ids"`
}

func newInferenceOpenAICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "openai",
		Short: "Manage OpenAI Workload Identity Federation enrollment",
		Long: `Commands for enrolling repositories with OpenAI Workload Identity
Federation. Generate a request document for your administrator,
import the reply into fullsend configuration, and check the
exchange status.

These commands do not call the OpenAI API. They produce documents
and update local configuration.`,
	}
	cmd.AddCommand(newInferenceOpenAIRequestCmd())
	cmd.AddCommand(newInferenceOpenAIImportCmd())
	cmd.AddCommand(newInferenceOpenAIStatusCmd())
	return cmd
}

func newInferenceOpenAIRequestCmd() *cobra.Command {
	var audience string
	var project string
	var serviceAccount string
	var format string
	var outFile string

	cmd := &cobra.Command{
		Use:   "request <owner/repo>[,<owner/repo>…]",
		Short: "Generate OpenAI WIF provider/mapping request",
		Long: `Generates the request document an administrator needs to enable
OpenAI Workload Identity Federation for one or more repositories.

Every value in the document is computed from the repository names.
Nothing is sent anywhere; the command needs no credentials.

Output formats:
  --format json   A stable, versioned JSON schema suitable for a
                  future API submission.
  --format md     A copy-paste ticket/email matching the guide's
                  route-B template.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repos, err := parseRepoList(args[0])
			if err != nil {
				return err
			}
			if len(repos) == 0 {
				return fmt.Errorf("at least one owner/repo is required")
			}

			// Derive owner from first repo for default audience.
			owner := strings.SplitN(repos[0], "/", 2)[0]
			if audience == "" {
				audience = defaultOpenAIAudiencePrefix + owner
			}

			doc := buildRequestDoc(repos, audience, project, serviceAccount)

			var output string
			switch format {
			case "json":
				b, err := json.MarshalIndent(doc, "", "  ")
				if err != nil {
					return fmt.Errorf("marshaling request JSON: %w", err)
				}
				output = string(b) + "\n"
			case "md":
				output, err = renderRequestMarkdown(doc)
				if err != nil {
					return fmt.Errorf("rendering request markdown: %w", err)
				}
			default:
				return fmt.Errorf("--format must be one of: json, md (got %q)", format)
			}

			if outFile != "" {
				if err := os.WriteFile(outFile, []byte(output), 0o644); err != nil {
					return fmt.Errorf("writing output to %s: %w", outFile, err)
				}
				printer := ui.New(cmd.OutOrStdout())
				printer.StepDone(fmt.Sprintf("Request written to %s", outFile))
				return nil
			}

			fmt.Fprint(cmd.OutOrStdout(), output)
			return nil
		},
	}

	cmd.Flags().StringVar(&audience, "audience", "", "OpenAI Workload Identity audience (default: fullsend://<owner>)")
	cmd.Flags().StringVar(&project, "project", "", "OpenAI project name or ID for the service accounts")
	cmd.Flags().StringVar(&serviceAccount, "service-account", "", "existing service account ID to map (default: create fullsend-<repo>-ci per repo)")
	cmd.Flags().StringVar(&format, "format", "json", "output format: json, md")
	cmd.Flags().StringVar(&outFile, "out", "", "write output to a file instead of stdout")

	return cmd
}

// parseRepoList splits a comma-separated list of owner/repo arguments
// and validates each one.
func parseRepoList(arg string) ([]string, error) {
	parts := strings.Split(arg, ",")
	var repos []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			return nil, fmt.Errorf("expected owner/repo format, got %q (org-only targets are not supported; specify the repository)", p)
		}
		_, _, err := parseOrgOrRepo(p)
		if err != nil {
			return nil, err
		}
		repos = append(repos, p)
	}
	return repos, nil
}

// defaultServiceAccountID derives the default service account name for
// a repository: fullsend-<repo>-ci.
func defaultServiceAccountID(repo string) string {
	parts := strings.SplitN(repo, "/", 2)
	repoName := parts[1]
	return "fullsend-" + repoName + "-ci"
}

func buildRequestDoc(repos []string, audience, project, serviceAccount string) openAIRequestDoc {
	doc := openAIRequestDoc{
		Version: openAIRequestSchemaVersion,
		Provider: openAIRequestProvider{
			Issuer:       githubOIDCIssuer,
			Audience:     audience,
			UploadedJWKS: false,
		},
		Reply: openAIRequestReply{
			IdentityProviderID: "",
			Audience:           audience,
			ServiceAccountIDs:  make(map[string]string),
		},
	}

	for _, repo := range repos {
		sa := serviceAccount
		if sa == "" {
			sa = defaultServiceAccountID(repo)
		}

		mapping := openAIRequestMapping{
			Repository: repo,
			Assertions: openAIRequestAssertions{
				Iss:        githubOIDCIssuer,
				Aud:        audience,
				Repository: repo,
				Ref:        openAIDefaultRef,
			},
			Target: openAIRequestTarget{
				Project:        project,
				ServiceAccount: sa,
				Permissions:    []string{openAIDefaultPermission},
			},
		}
		doc.Mappings = append(doc.Mappings, mapping)
		doc.Reply.ServiceAccountIDs[repo] = ""
	}

	return doc
}

// requestMarkdownTmpl matches the guide's route-B template structure.
var requestMarkdownTmpl = template.Must(template.New("request").Parse(`# OpenAI Workload Identity Federation Request

## Provider (reuse or create)

| Field | Value |
|---|---|
| OIDC issuer URL | ` + "`" + `{{ .Provider.Issuer }}` + "`" + ` |
| Audience | ` + "`" + `{{ .Provider.Audience }}` + "`" + ` |
| Use uploaded JWKS for token verification | **Off** |

If the organization already has a provider with a different audience,
keep its audience and report it back (see Reply below).

## Service account mappings

One mapping per repository. Every assertion must be exact — no wildcards,
no ` + "`" + `repository_owner` + "`" + `, no ` + "`" + `workflow_ref` + "`" + ` (fullsend starts agent runs
from seven workflow files). Do **not** create an API key for the service account.
{{ range .Mappings }}
### {{ .Repository }}

| Field | Value |
|---|---|
| Claim assertions | ` + "`" + `iss` + "`" + ` = ` + "`" + `{{ .Assertions.Iss }}` + "`" + ` · ` + "`" + `aud` + "`" + ` = ` + "`" + `{{ .Assertions.Aud }}` + "`" + ` · ` + "`" + `repository` + "`" + ` = ` + "`" + `{{ .Assertions.Repository }}` + "`" + ` · ` + "`" + `ref` + "`" + ` = ` + "`" + `{{ .Assertions.Ref }}` + "`" + ` |
| Project | {{ if .Target.Project }}` + "`" + `{{ .Target.Project }}` + "`" + `{{ else }}*(specify the project name or ID)*{{ end }} |
| Service account | {{ .Target.ServiceAccount }} (create inline in the mapping) |
| Permissions | ` + "`" + `{{ index .Target.Permissions 0 }}` + "`" + ` only |
{{ end }}
## Reply

Please provide the following identifiers so we can configure the repository:

| Identifier | Value |
|---|---|
| Identity provider ID | *(from the provider you created or reused)* |
| Provider's audience | ` + "`" + `{{ .Reply.Audience }}` + "`" + ` *(confirm or update if different)* |
{{ range .Mappings }}| Service account ID for {{ .Repository }} | *(from the mapping above)* |
{{ end }}
These identifiers are not secrets — they grant nothing on their own.
The mapping only trusts a GitHub OIDC token whose claims match.
`))

func renderRequestMarkdown(doc openAIRequestDoc) (string, error) {
	var sb strings.Builder
	if err := requestMarkdownTmpl.Execute(&sb, doc); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// --- import command ---

// openAIReplyDoc is the JSON shape accepted by the import command. It
// mirrors the request schema's reply section so an admin who fills the
// JSON in can be imported directly.
type openAIReplyDoc struct {
	IdentityProviderID string            `json:"identity_provider_id"`
	Audience           string            `json:"audience"`
	ServiceAccountID   string            `json:"service_account_id,omitempty"`
	ServiceAccountIDs  map[string]string `json:"service_account_ids,omitempty"`
}

func newInferenceOpenAIImportCmd() *cobra.Command {
	var (
		flagAudience           string
		flagIdentityProviderID string
		flagServiceAccountID   string
		fullsendDir            string
		variables              bool
		repo                   string
	)

	cmd := &cobra.Command{
		Use:   "import [reply.json]",
		Short: "Import OpenAI WIF identifiers into fullsend config",
		Long: `Takes the administrator's reply and writes inference.openai into
.fullsend/config.yaml through the same setters as
'fullsend github setup --openai-*'.

The reply can be provided as a JSON file argument or via flags.
All three identifiers (audience, identity-provider-id,
service-account-id) must be present — a partial trio is refused.

With --variables, sets the three FULLSEND_OPENAI_* repository
variables instead of writing to config.yaml (requires a GitHub
token with variable-write permissions and --repo).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(cmd.OutOrStdout())

			ids, err := resolveImportIDs(args, flagAudience, flagIdentityProviderID, flagServiceAccountID)
			if err != nil {
				return err
			}

			if err := validateImportIDs(ids); err != nil {
				return err
			}

			if variables {
				return runImportVariables(printer, ids, repo)
			}

			return runImportConfig(printer, ids, fullsendDir)
		},
	}

	cmd.Flags().StringVar(&flagAudience, "audience", "", "OpenAI Workload Identity audience")
	cmd.Flags().StringVar(&flagIdentityProviderID, "identity-provider-id", "", "OpenAI identity provider ID")
	cmd.Flags().StringVar(&flagServiceAccountID, "service-account-id", "", "OpenAI service account ID")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", ".fullsend", "path to the .fullsend configuration directory")
	cmd.Flags().BoolVar(&variables, "variables", false, "set FULLSEND_OPENAI_* repository variables instead of writing config.yaml (requires --repo)")
	cmd.Flags().StringVar(&repo, "repo", "", "target repository (owner/repo) for --variables")

	return cmd
}

// resolveImportIDs takes the command arguments and flags and returns the
// OpenAI WIF config. Flags take precedence over the JSON file.
func resolveImportIDs(args []string, flagAudience, flagIdentityProviderID, flagServiceAccountID string) (config.OpenAIWIFConfig, error) {
	var ids config.OpenAIWIFConfig

	// Load from JSON file if provided.
	if len(args) == 1 {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return ids, fmt.Errorf("reading reply file: %w", err)
		}
		var reply openAIReplyDoc
		if err := json.Unmarshal(data, &reply); err != nil {
			return ids, fmt.Errorf("parsing reply JSON: %w", err)
		}
		ids.Audience = reply.Audience
		ids.IdentityProviderID = reply.IdentityProviderID
		// Single service_account_id takes precedence; for multi-repo
		// replies the caller must use --service-account-id to select.
		if reply.ServiceAccountID != "" {
			ids.ServiceAccountID = reply.ServiceAccountID
		} else if len(reply.ServiceAccountIDs) == 1 {
			for _, v := range reply.ServiceAccountIDs {
				ids.ServiceAccountID = v
			}
		}
	}

	// Flags override file values.
	if flagAudience != "" {
		ids.Audience = flagAudience
	}
	if flagIdentityProviderID != "" {
		ids.IdentityProviderID = flagIdentityProviderID
	}
	if flagServiceAccountID != "" {
		ids.ServiceAccountID = flagServiceAccountID
	}

	return ids.Trimmed(), nil
}

// validateImportIDs enforces the all-three-or-none rule.
func validateImportIDs(ids config.OpenAIWIFConfig) error {
	if ids.IsZero() {
		return fmt.Errorf("no identifiers provided: pass a reply JSON file or all three flags (--audience, --identity-provider-id, --service-account-id)")
	}
	if missing := ids.Missing(); len(missing) > 0 {
		return fmt.Errorf("--audience, --identity-provider-id, and --service-account-id must all be set (missing %s)", strings.Join(missing, ", "))
	}
	return nil
}

func runImportConfig(printer *ui.Printer, ids config.OpenAIWIFConfig, fullsendDir string) error {
	writer, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return fmt.Errorf("loading config from %s: %w", fullsendDir, err)
	}

	perRepo, ok := writer.(config.PerRepoConfigWriter)
	if !ok {
		return fmt.Errorf("inference openai import writes per-repo config; %s contains an org-mode config", fullsendDir)
	}

	perRepo.SetInferenceOpenAI(ids)

	data, err := perRepo.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	configPath := filepath.Join(fullsendDir, "config.yaml")
	if err := os.MkdirAll(fullsendDir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", fullsendDir, err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fmt.Errorf("writing config to %s: %w", configPath, err)
	}

	printer.StepDone("Wrote inference.openai to " + configPath)
	printer.KeyValue("audience", ids.Audience)
	printer.KeyValue("identity_provider_id", ids.IdentityProviderID)
	printer.KeyValue("service_account_id", ids.ServiceAccountID)

	return nil
}

// ghRunner runs a gh CLI command and returns its combined output. Tests
// can replace this variable with a stub.
var ghRunner = func(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runImportVariables(printer *ui.Printer, ids config.OpenAIWIFConfig, repo string) error {
	if repo == "" {
		return fmt.Errorf("--repo is required when using --variables")
	}
	if !strings.Contains(repo, "/") {
		return fmt.Errorf("--repo must be in owner/repo format")
	}

	parts := strings.SplitN(repo, "/", 2)
	owner, repoName := parts[0], parts[1]

	if !githubOwnerPattern.MatchString(owner) {
		return fmt.Errorf("invalid owner name %q", owner)
	}
	if !githubRepoPattern.MatchString(repoName) {
		return fmt.Errorf("invalid repo name %q", repoName)
	}

	type varEntry struct {
		name  string
		value string
	}
	vars := []varEntry{
		{openAIAudienceEnv, ids.Audience},
		{openAIIdentityProviderIDEnv, ids.IdentityProviderID},
		{openAIServiceAccountIDEnv, ids.ServiceAccountID},
	}

	for _, v := range vars {
		ghArgs := []string{"variable", "set", v.name, "--repo", repo, "--body", v.value}
		printer.StepStart(fmt.Sprintf("Setting %s on %s", v.name, repo))
		out, err := ghRunner(ghArgs...)
		if err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set %s", v.name))
			return fmt.Errorf("setting variable %s on %s: %w\n%s", v.name, repo, err, out)
		}
		printer.StepDone(fmt.Sprintf("Set %s on %s", v.name, repo))
	}

	return nil
}

// --- status command ---

func newInferenceOpenAIStatusCmd() *cobra.Command {
	var fullsendDir string

	cmd := &cobra.Command{
		Use:   "status <owner/repo>",
		Short: "Check OpenAI WIF configuration and exchange status",
		Long: `Prints the resolved OpenAI WIF identifiers and their source
(config.yaml or environment variables), and flags a partial trio.

When run inside a GitHub Actions job with id-token: write, performs
one exchange through internal/inference/openaiwif and reports the
returned scope and expiry (the same code path as 'fullsend run')
without ever printing the token. Outside Actions, says so and stops
at the config checks.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, repo, err := parseOrgOrRepo(args[0])
			if err != nil {
				return err
			}
			if repo == "" {
				return fmt.Errorf("expected owner/repo format, got org-only %q", args[0])
			}

			printer := ui.New(cmd.OutOrStdout())
			return runInferenceOpenAIStatus(cmd, printer, repo, fullsendDir)
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", ".fullsend", "path to the .fullsend configuration directory")

	return cmd
}

// openAIStatusSource describes where an identifier was resolved from.
type openAIStatusSource struct {
	Audience           string
	AudienceSource     string
	IdentityProviderID string
	IDPSource          string
	ServiceAccountID   string
	SASource           string
}

func resolveOpenAIStatusSources(fullsendDir string) openAIStatusSource {
	var s openAIStatusSource

	// Check environment variables first (they take precedence).
	envAud := strings.TrimSpace(os.Getenv(openAIAudienceEnv))
	envIDP := strings.TrimSpace(os.Getenv(openAIIdentityProviderIDEnv))
	envSA := strings.TrimSpace(os.Getenv(openAIServiceAccountIDEnv))

	// Load config as fallback.
	var cfgIDs config.OpenAIWIFConfig
	writer, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{MissingOK: true})
	if err == nil {
		if perRepo, ok := writer.(config.PerRepoConfigReader); ok {
			cfgIDs = perRepo.ConfigInferenceOpenAI().Trimmed()
		}
	}

	// Resolve each field: env var wins, then config.
	if envAud != "" {
		s.Audience = envAud
		s.AudienceSource = "variable " + openAIAudienceEnv
	} else if cfgIDs.Audience != "" {
		s.Audience = cfgIDs.Audience
		s.AudienceSource = "config.yaml"
	}

	if envIDP != "" {
		s.IdentityProviderID = envIDP
		s.IDPSource = "variable " + openAIIdentityProviderIDEnv
	} else if cfgIDs.IdentityProviderID != "" {
		s.IdentityProviderID = cfgIDs.IdentityProviderID
		s.IDPSource = "config.yaml"
	}

	if envSA != "" {
		s.ServiceAccountID = envSA
		s.SASource = "variable " + openAIServiceAccountIDEnv
	} else if cfgIDs.ServiceAccountID != "" {
		s.ServiceAccountID = cfgIDs.ServiceAccountID
		s.SASource = "config.yaml"
	}

	return s
}

func runInferenceOpenAIStatus(cmd *cobra.Command, printer *ui.Printer, repo, fullsendDir string) error {
	printer.Banner(Version())
	printer.Blank()
	printer.Header("OpenAI WIF Status: " + repo)
	printer.Blank()

	sources := resolveOpenAIStatusSources(fullsendDir)

	// Print resolved identifiers.
	printOpenAIStatusField(printer, "audience", sources.Audience, sources.AudienceSource)
	printOpenAIStatusField(printer, "identity_provider_id", sources.IdentityProviderID, sources.IDPSource)
	printOpenAIStatusField(printer, "service_account_id", sources.ServiceAccountID, sources.SASource)
	printer.Blank()

	// Check completeness.
	ids := config.OpenAIWIFConfig{
		Audience:           sources.Audience,
		IdentityProviderID: sources.IdentityProviderID,
		ServiceAccountID:   sources.ServiceAccountID,
	}

	if ids.IsZero() {
		printer.StepFail("No OpenAI WIF identifiers configured")
		printer.StepInfo("Run 'fullsend inference openai import' or 'fullsend github setup --openai-*' to configure")
		return nil
	}

	if missing := ids.Missing(); len(missing) > 0 {
		printer.StepWarn(fmt.Sprintf("Partial trio: missing %s", strings.Join(missing, ", ")))
		printer.StepInfo("All three identifiers must be set for the exchange to work")
		return nil
	}

	printer.StepDone("All three identifiers are set")
	printer.Blank()

	// Check if we're inside GitHub Actions with OIDC.
	oidcURL := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
	oidcToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")

	if oidcURL == "" || oidcToken == "" {
		printer.StepInfo("Not inside a GitHub Actions job with id-token: write")
		printer.StepInfo("The exchange can only be tested from a GitHub Actions workflow")
		return nil
	}

	// Perform the exchange.
	printer.StepStart("Performing WIF exchange")
	tok, err := openAIExchange(cmd.Context(), openaiwif.Config{
		Audience:           ids.Audience,
		IdentityProviderID: ids.IdentityProviderID,
		ServiceAccountID:   ids.ServiceAccountID,
		OIDCRequestURL:     oidcURL,
		OIDCRequestToken:   oidcToken,
	})
	if err != nil {
		printer.StepFail("Exchange failed")
		// Never print the token in error messages — openaiwif already
		// ensures that, but be explicit.
		return fmt.Errorf("OpenAI WIF exchange: %w", err)
	}

	printer.StepDone("Exchange succeeded")
	printer.Blank()

	scope := tok.Scope
	if scope == "" {
		scope = "(not narrowed)"
	}
	printer.KeyValue("scope", scope)
	printer.KeyValue("expires_in", time.Until(tok.ExpiresAt).Round(time.Second).String())

	if warning, err := checkOpenAIScope(tok.Scope); err != nil {
		printer.StepWarn(err.Error())
	} else if warning != "" {
		printer.StepWarn(warning)
	}

	return nil
}

func printOpenAIStatusField(printer *ui.Printer, name, value, source string) {
	if value == "" {
		printer.StepInfo(fmt.Sprintf("%s: (not set)", name))
	} else {
		printer.StepInfo(fmt.Sprintf("%s: %s (from %s)", name, value, source))
	}
}
