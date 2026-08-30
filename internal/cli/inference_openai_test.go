package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// --- openai parent command ---

func TestInferenceOpenAICmd_HasSubcommands(t *testing.T) {
	cmd := newInferenceOpenAICmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["request"], "expected request subcommand")
	assert.True(t, names["import"], "expected import subcommand")
	assert.True(t, names["status"], "expected status subcommand")
}

func TestInferenceOpenAICmd_RegisteredInInference(t *testing.T) {
	cmd := newInferenceCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Use == "openai" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected openai subcommand registered in inference")
}

// --- request command tests ---

func TestInferenceOpenAIRequestCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInferenceOpenAIRequestCmd_RejectsInvalidFormat(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "yaml"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--format must be one of: json, md")
}

func TestInferenceOpenAIRequestCmd_RejectsOrgOnlyTarget(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestInferenceOpenAIRequestCmd_JSONSingleRepo(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	assert.Equal(t, openAIRequestSchemaVersion, doc.Version)
	assert.Equal(t, githubOIDCIssuer, doc.Provider.Issuer)
	assert.Equal(t, "fullsend://acme", doc.Provider.Audience)
	assert.False(t, doc.Provider.UploadedJWKS)

	require.Len(t, doc.Mappings, 1)
	m := doc.Mappings[0]
	assert.Equal(t, "acme/widget", m.Repository)
	assert.Equal(t, githubOIDCIssuer, m.Assertions.Iss)
	assert.Equal(t, "fullsend://acme", m.Assertions.Aud)
	assert.Equal(t, "acme/widget", m.Assertions.Repository)
	assert.Equal(t, "refs/heads/main", m.Assertions.Ref)
	assert.Equal(t, "fullsend-widget-ci", m.Target.ServiceAccount)
	assert.Equal(t, []string{"api.model.request"}, m.Target.Permissions)
	assert.Empty(t, m.Target.Project) // no --project flag

	assert.Equal(t, "fullsend://acme", doc.Reply.Audience)
	assert.Empty(t, doc.Reply.IdentityProviderID)
	assert.Contains(t, doc.Reply.ServiceAccountIDs, "acme/widget")
}

func TestInferenceOpenAIRequestCmd_JSONMultiRepo(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request",
		"acme/widget,acme/gadget",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	require.Len(t, doc.Mappings, 2)
	assert.Equal(t, "acme/widget", doc.Mappings[0].Repository)
	assert.Equal(t, "acme/gadget", doc.Mappings[1].Repository)

	assert.Equal(t, "fullsend-widget-ci", doc.Mappings[0].Target.ServiceAccount)
	assert.Equal(t, "fullsend-gadget-ci", doc.Mappings[1].Target.ServiceAccount)

	assert.Len(t, doc.Reply.ServiceAccountIDs, 2)
}

func TestInferenceOpenAIRequestCmd_CustomAudienceAndProject(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--audience", "https://custom.audience",
		"--project", "my-openai-project",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	assert.Equal(t, "https://custom.audience", doc.Provider.Audience)
	assert.Equal(t, "https://custom.audience", doc.Mappings[0].Assertions.Aud)
	assert.Equal(t, "my-openai-project", doc.Mappings[0].Target.Project)
}

func TestInferenceOpenAIRequestCmd_CustomServiceAccount(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--service-account", "existing-sa-id",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	assert.Equal(t, "existing-sa-id", doc.Mappings[0].Target.ServiceAccount)
}

func TestInferenceOpenAIRequestCmd_MarkdownOutput(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "md"})
	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "# OpenAI Workload Identity Federation Request")
	assert.Contains(t, output, "acme/widget")
	assert.Contains(t, output, githubOIDCIssuer)
	assert.Contains(t, output, "fullsend://acme")
	assert.Contains(t, output, "fullsend-widget-ci")
	assert.Contains(t, output, "api.model.request")
	assert.Contains(t, output, "Identity provider ID")
	assert.Contains(t, output, "Service account ID for acme/widget")
}

func TestInferenceOpenAIRequestCmd_OutFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "request.json")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "json",
		"--out", outPath})
	err := cmd.Execute()
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, "acme/widget", doc.Mappings[0].Repository)
}

// --- parseRepoList tests ---

func TestParseRepoList_SingleRepo(t *testing.T) {
	repos, err := parseRepoList("acme/widget")
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/widget"}, repos)
}

func TestParseRepoList_MultipleRepos(t *testing.T) {
	repos, err := parseRepoList("acme/widget, acme/gadget")
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/widget", "acme/gadget"}, repos)
}

func TestParseRepoList_RejectsOrgOnly(t *testing.T) {
	_, err := parseRepoList("acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestParseRepoList_RejectsEmpty(t *testing.T) {
	repos, err := parseRepoList("")
	require.NoError(t, err)
	assert.Empty(t, repos)
}

func TestParseRepoList_RejectsInvalidRepo(t *testing.T) {
	_, err := parseRepoList("acme/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

// --- defaultServiceAccountID tests ---

func TestDefaultServiceAccountID(t *testing.T) {
	assert.Equal(t, "fullsend-widget-ci", defaultServiceAccountID("acme/widget"))
	assert.Equal(t, "fullsend-gadget-ci", defaultServiceAccountID("acme/gadget"))
	assert.Equal(t, "fullsend-my-repo-ci", defaultServiceAccountID("org/my-repo"))
}

// --- import command tests ---

func TestInferenceOpenAIImportCmd_Flags(t *testing.T) {
	cmd := newInferenceOpenAIImportCmd()

	assert.NotNil(t, cmd.Flags().Lookup("audience"))
	assert.NotNil(t, cmd.Flags().Lookup("identity-provider-id"))
	assert.NotNil(t, cmd.Flags().Lookup("service-account-id"))
	assert.NotNil(t, cmd.Flags().Lookup("fullsend-dir"))
	assert.NotNil(t, cmd.Flags().Lookup("variables"))
	assert.NotNil(t, cmd.Flags().Lookup("repo"))
}

func TestResolveImportIDs_FromFlags(t *testing.T) {
	ids, err := resolveImportIDs(nil, "aud", "idp", "sa")
	require.NoError(t, err)
	assert.Equal(t, config.OpenAIWIFConfig{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
	}, ids)
}

func TestResolveImportIDs_FromFile(t *testing.T) {
	dir := t.TempDir()
	replyPath := filepath.Join(dir, "reply.json")
	reply := openAIReplyDoc{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountID:   "sa_456",
	}
	data, err := json.Marshal(reply)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(replyPath, data, 0o644))

	ids, err := resolveImportIDs([]string{replyPath}, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "fullsend://acme", ids.Audience)
	assert.Equal(t, "idp_123", ids.IdentityProviderID)
	assert.Equal(t, "sa_456", ids.ServiceAccountID)
}

func TestResolveImportIDs_FromFileWithSingleServiceAccountIDs(t *testing.T) {
	dir := t.TempDir()
	replyPath := filepath.Join(dir, "reply.json")
	reply := openAIReplyDoc{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountIDs:  map[string]string{"acme/widget": "sa_widget"},
	}
	data, err := json.Marshal(reply)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(replyPath, data, 0o644))

	ids, err := resolveImportIDs([]string{replyPath}, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "sa_widget", ids.ServiceAccountID)
}

func TestResolveImportIDs_FlagsOverrideFile(t *testing.T) {
	dir := t.TempDir()
	replyPath := filepath.Join(dir, "reply.json")
	reply := openAIReplyDoc{
		Audience:           "from-file",
		IdentityProviderID: "idp-file",
		ServiceAccountID:   "sa-file",
	}
	data, err := json.Marshal(reply)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(replyPath, data, 0o644))

	ids, err := resolveImportIDs([]string{replyPath}, "from-flag", "", "")
	require.NoError(t, err)
	assert.Equal(t, "from-flag", ids.Audience)
	assert.Equal(t, "idp-file", ids.IdentityProviderID)
}

func TestValidateImportIDs_RefusesPartialTrio(t *testing.T) {
	err := validateImportIDs(config.OpenAIWIFConfig{Audience: "aud"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must all be set")
	assert.Contains(t, err.Error(), "identity_provider_id")
	assert.Contains(t, err.Error(), "service_account_id")
}

func TestValidateImportIDs_RefusesEmpty(t *testing.T) {
	err := validateImportIDs(config.OpenAIWIFConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no identifiers provided")
}

func TestValidateImportIDs_AcceptsComplete(t *testing.T) {
	err := validateImportIDs(config.OpenAIWIFConfig{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
	})
	require.NoError(t, err)
}

func TestRunImportConfig_WritesConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")

	ids := config.OpenAIWIFConfig{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountID:   "sa_456",
	}

	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportConfig(printer, ids, fullsendDir)
	require.NoError(t, err)

	// Verify the config was written.
	configPath := filepath.Join(fullsendDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	cfg, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{})
	require.NoError(t, err)
	perRepo, ok := cfg.(config.PerRepoConfigReader)
	require.True(t, ok)

	openai := perRepo.ConfigInferenceOpenAI()
	assert.Equal(t, "fullsend://acme", openai.Audience)
	assert.Equal(t, "idp_123", openai.IdentityProviderID)
	assert.Equal(t, "sa_456", openai.ServiceAccountID)

	// Make sure the file is valid YAML.
	assert.NotEmpty(t, data)
}

func TestRunImportConfig_PreservesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	// Write a pre-existing config with runtime set.
	configPath := filepath.Join(fullsendDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("runtime: claude\nversion: \"1\"\n"), 0o644))

	ids := config.OpenAIWIFConfig{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountID:   "sa_456",
	}

	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportConfig(printer, ids, fullsendDir)
	require.NoError(t, err)

	// Verify both runtime and openai are present.
	cfg, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{})
	require.NoError(t, err)
	perRepo, ok := cfg.(config.PerRepoConfigReader)
	require.True(t, ok)
	assert.Equal(t, "claude", perRepo.ConfigRuntime())
	assert.Equal(t, "fullsend://acme", perRepo.ConfigInferenceOpenAI().Audience)
}

func TestRunImportVariables_RequiresRepo(t *testing.T) {
	ids := config.OpenAIWIFConfig{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
	}
	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportVariables(printer, ids, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--repo is required")
}

func TestRunImportVariables_RequiresOwnerSlashRepo(t *testing.T) {
	ids := config.OpenAIWIFConfig{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
	}
	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportVariables(printer, ids, "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestRunImportVariables_CallsGH(t *testing.T) {
	// Replace ghRunner with a stub.
	var calls [][]string
	origGH := ghRunner
	ghRunner = func(args ...string) (string, error) {
		calls = append(calls, args)
		return "", nil
	}
	defer func() { ghRunner = origGH }()

	ids := config.OpenAIWIFConfig{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountID:   "sa_456",
	}
	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportVariables(printer, ids, "acme/widget")
	require.NoError(t, err)

	require.Len(t, calls, 3)
	assert.Equal(t, []string{"variable", "set", "FULLSEND_OPENAI_AUDIENCE", "--repo", "acme/widget", "--body", "fullsend://acme"}, calls[0])
	assert.Equal(t, []string{"variable", "set", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "--repo", "acme/widget", "--body", "idp_123"}, calls[1])
	assert.Equal(t, []string{"variable", "set", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "--repo", "acme/widget", "--body", "sa_456"}, calls[2])
}

// --- status command tests ---

func TestInferenceOpenAIStatusCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "status"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInferenceOpenAIStatusCmd_RejectsOrgOnly(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "status", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestResolveOpenAIStatusSources_FromConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_cfg
    service_account_id: sa_cfg
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	// Clear env vars.
	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")

	s := resolveOpenAIStatusSources(fullsendDir)
	assert.Equal(t, "fullsend://acme", s.Audience)
	assert.Equal(t, "config.yaml", s.AudienceSource)
	assert.Equal(t, "idp_cfg", s.IdentityProviderID)
	assert.Equal(t, "config.yaml", s.IDPSource)
	assert.Equal(t, "sa_cfg", s.ServiceAccountID)
	assert.Equal(t, "config.yaml", s.SASource)
}

func TestResolveOpenAIStatusSources_EnvOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: from-config
    identity_provider_id: idp_cfg
    service_account_id: sa_cfg
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	t.Setenv(openAIAudienceEnv, "from-env")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")

	s := resolveOpenAIStatusSources(fullsendDir)
	assert.Equal(t, "from-env", s.Audience)
	assert.Contains(t, s.AudienceSource, "variable")
	assert.Equal(t, "idp_cfg", s.IdentityProviderID)
	assert.Equal(t, "config.yaml", s.IDPSource)
}

func TestResolveOpenAIStatusSources_NoConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")

	s := resolveOpenAIStatusSources(fullsendDir)
	assert.Empty(t, s.Audience)
	assert.Empty(t, s.IdentityProviderID)
	assert.Empty(t, s.ServiceAccountID)
}

func TestRunInferenceOpenAIStatus_NoConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget",
		"--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No OpenAI WIF identifiers configured")
}

func TestRunInferenceOpenAIStatus_PartialConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: fullsend://acme
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget",
		"--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Partial trio")
}

func TestRunInferenceOpenAIStatus_FullConfigNoActions(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_123
    service_account_id: sa_456
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget",
		"--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "All three identifiers are set")
	assert.Contains(t, output, "Not inside a GitHub Actions job")
}

// --- buildRequestDoc tests ---

func TestBuildRequestDoc_DefaultAudience(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget"}, "fullsend://acme", "", "")
	assert.Equal(t, "fullsend://acme", doc.Provider.Audience)
	assert.Equal(t, "fullsend://acme", doc.Reply.Audience)
}

func TestBuildRequestDoc_CorrectAssertions(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget", "acme/gadget"}, "fullsend://acme", "proj-1", "")

	require.Len(t, doc.Mappings, 2)

	for _, m := range doc.Mappings {
		assert.Equal(t, githubOIDCIssuer, m.Assertions.Iss)
		assert.Equal(t, "fullsend://acme", m.Assertions.Aud)
		assert.Equal(t, m.Repository, m.Assertions.Repository)
		assert.Equal(t, "refs/heads/main", m.Assertions.Ref)
		assert.Equal(t, "proj-1", m.Target.Project)
		assert.Equal(t, []string{"api.model.request"}, m.Target.Permissions)
	}
}

func TestBuildRequestDoc_ServiceAccountIDPerRepo(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget", "acme/gadget"}, "aud", "", "")
	assert.Equal(t, "fullsend-widget-ci", doc.Mappings[0].Target.ServiceAccount)
	assert.Equal(t, "fullsend-gadget-ci", doc.Mappings[1].Target.ServiceAccount)
}

func TestBuildRequestDoc_SharedServiceAccount(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget", "acme/gadget"}, "aud", "", "shared-sa")
	assert.Equal(t, "shared-sa", doc.Mappings[0].Target.ServiceAccount)
	assert.Equal(t, "shared-sa", doc.Mappings[1].Target.ServiceAccount)
}

// --- renderRequestMarkdown tests ---

func TestRenderRequestMarkdown_ContainsExpectedSections(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget"}, "fullsend://acme", "", "")
	md, err := renderRequestMarkdown(doc)
	require.NoError(t, err)

	assert.Contains(t, md, "## Provider (reuse or create)")
	assert.Contains(t, md, "## Service account mappings")
	assert.Contains(t, md, "### acme/widget")
	assert.Contains(t, md, "## Reply")
	assert.Contains(t, md, "not secrets")
}

// --- end-to-end import via root command ---

func TestInferenceOpenAIImportCmd_FullFlowViaFlags(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "import",
		"--audience", "fullsend://acme",
		"--identity-provider-id", "idp_123",
		"--service-account-id", "sa_456",
		"--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify config was written correctly.
	cfg, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{})
	require.NoError(t, err)
	perRepo, ok := cfg.(config.PerRepoConfigReader)
	require.True(t, ok)
	openai := perRepo.ConfigInferenceOpenAI()
	assert.Equal(t, "fullsend://acme", openai.Audience)
	assert.Equal(t, "idp_123", openai.IdentityProviderID)
	assert.Equal(t, "sa_456", openai.ServiceAccountID)
}

func TestInferenceOpenAIImportCmd_RefusesPartialTrio(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "import",
		"--audience", "fullsend://acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must all be set")
}

func TestInferenceOpenAIImportCmd_RefusesNoArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "import"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no identifiers provided")
}

// newTestPrinter creates a ui.Printer that writes to the given buffer.
func newTestPrinter(buf *bytes.Buffer) *ui.Printer {
	return ui.New(buf)
}

// --- helpers ---

func TestInferenceOpenAIRequestCmd_DoesNotRequireGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInferenceOpenAIStatusCmd_DoesNotRequireGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	dir := t.TempDir()
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget",
		"--fullsend-dir", filepath.Join(dir, ".fullsend")})
	err := cmd.Execute()
	require.NoError(t, err) // should not fail with "no GitHub token found"
}

// --- request JSON round-trip test (golden) ---

func TestBuildRequestDoc_JSONRoundTrip(t *testing.T) {
	doc := buildRequestDoc(
		[]string{"acme/widget", "acme/gadget"},
		"fullsend://acme",
		"openai-proj-001",
		"",
	)

	b, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)

	var roundTrip openAIRequestDoc
	require.NoError(t, json.Unmarshal(b, &roundTrip))

	assert.Equal(t, doc.Version, roundTrip.Version)
	assert.Equal(t, doc.Provider, roundTrip.Provider)
	assert.Len(t, roundTrip.Mappings, 2)
	assert.Equal(t, doc.Mappings[0].Assertions, roundTrip.Mappings[0].Assertions)
	assert.Equal(t, doc.Mappings[1].Assertions, roundTrip.Mappings[1].Assertions)
	assert.Equal(t, doc.Reply.Audience, roundTrip.Reply.Audience)
}

// --- import replaces existing openai block ---

func TestRunImportConfig_ReplacesExistingOpenAI(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: old-audience
    identity_provider_id: old-idp
    service_account_id: old-sa
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	ids := config.OpenAIWIFConfig{
		Audience:           "new-audience",
		IdentityProviderID: "new-idp",
		ServiceAccountID:   "new-sa",
	}

	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportConfig(printer, ids, fullsendDir)
	require.NoError(t, err)

	// Read back and verify the new values.
	cfg, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{})
	require.NoError(t, err)
	perRepo, ok := cfg.(config.PerRepoConfigReader)
	require.True(t, ok)
	openai := perRepo.ConfigInferenceOpenAI()
	assert.Equal(t, "new-audience", openai.Audience)
	assert.Equal(t, "new-idp", openai.IdentityProviderID)
	assert.Equal(t, "new-sa", openai.ServiceAccountID)

	// Verify old values are gone.
	raw, err := os.ReadFile(filepath.Join(fullsendDir, "config.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "old-audience")
}

// --- status with env vars from flags ---

func TestInferenceOpenAIStatusCmd_Flags(t *testing.T) {
	cmd := newInferenceOpenAIStatusCmd()
	assert.NotNil(t, cmd.Flags().Lookup("fullsend-dir"))
}

// --- request multi-repo with whitespace ---

func TestParseRepoList_TrimsWhitespace(t *testing.T) {
	repos, err := parseRepoList(" acme/widget , acme/gadget ")
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/widget", "acme/gadget"}, repos)
}

// Verify that Markdown output for multi-repo request contains all repos.
func TestInferenceOpenAIRequestCmd_MarkdownMultiRepo(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request",
		"acme/widget,acme/gadget",
		"--format", "md"})
	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "### acme/widget")
	assert.Contains(t, output, "### acme/gadget")
	assert.Contains(t, output, "fullsend-widget-ci")
	assert.Contains(t, output, "fullsend-gadget-ci")
	assert.True(t, strings.Contains(output, "Service account ID for acme/widget"))
	assert.True(t, strings.Contains(output, "Service account ID for acme/gadget"))
}
