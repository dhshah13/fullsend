package repos

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func TestBuildScaffoldPRMetadata_FreshInstall(t *testing.T) {
	fc := forge.NewFakeClient()
	notInstalled := false
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &notInstalled})

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "adds the fullsend scaffold files")
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_FreshInstallNoOpts(t *testing.T) {
	fc := forge.NewFakeClient()
	// No opts → defaults to fresh install.
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0")

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_UpgradeWithBothVersions(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents = map[string][]byte{
		"acme/widget/.github/workflows/fullsend.yaml": []byte(
			"uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc123 # v0.25.2\n"),
	}
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed})

	assert.Equal(t, "chore: bump fullsend from v0.25.2 to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "chore: bump fullsend from v0.25.2 to v0.28.0", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "from v0.25.2 to v0.28.0")
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_UpgradeWithNewVersionOnly(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed})

	assert.Equal(t, "chore: bump fullsend to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "chore: bump fullsend to v0.28.0", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "to v0.28.0")
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_UpgradeWithNoVersions(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "",
		ScaffoldMetadataOpts{GuardInstalled: &installed})

	assert.Equal(t, "chore: update fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "chore: update fullsend per-repo installation", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "updates the fullsend scaffold files")
	assert.Equal(t, DefaultScaffoldBranch, meta.Branch)
}

func TestBuildScaffoldPRMetadata_NilGuardDefaultsFresh(t *testing.T) {
	fc := forge.NewFakeClient()
	// GuardInstalled nil → defaults to fresh install.
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{})

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_PreFetchedGuardInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed})

	assert.Equal(t, "chore: bump fullsend to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_PreFetchedGuardNotInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	notInstalled := false
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &notInstalled})

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_PreFetchedOldVersion(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed, OldVersion: "v0.25.2"})

	assert.Equal(t, "chore: bump fullsend from v0.25.2 to v0.28.0", meta.CommitMsg)
	assert.Contains(t, meta.PRBody, "from v0.25.2 to v0.28.0")
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_PreFetchedBothGuardAndVersion(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed, OldVersion: "v0.24.0"})

	assert.Equal(t, "chore: bump fullsend from v0.24.0 to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestDetectExistingVersion(t *testing.T) {
	t.Run("version comment found", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents = map[string][]byte{
			"acme/widget/.github/workflows/fullsend.yaml": []byte(
				"name: fullsend\non:\n  workflow_dispatch:\njobs:\n  dispatch:\n    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@deadbeef # v0.25.2\n"),
		}
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "v0.25.2", v)
	})

	t.Run("no version comment", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents = map[string][]byte{
			"acme/widget/.github/workflows/fullsend.yaml": []byte(
				"name: fullsend\non:\n  workflow_dispatch:\n"),
		}
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "", v)
	})

	t.Run("file not found", func(t *testing.T) {
		fc := forge.NewFakeClient()
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "", v)
	})

	t.Run("prerelease version", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents = map[string][]byte{
			"acme/widget/.github/workflows/fullsend.yaml": []byte(
				"uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc # v1.0.0-rc.1\n"),
		}
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "v1.0.0-rc.1", v)
	})

	t.Run("hyphenated prerelease version", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents = map[string][]byte{
			"acme/widget/.github/workflows/fullsend.yaml": []byte(
				"uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc # v1.0.0-alpha-1\n"),
		}
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "v1.0.0-alpha-1", v)
	})
}

// commandsNotInPerRepoCatalog mirrors commandsNotInOnboardingCatalog in the
// scaffold package: dispatch.yml routes these but they are deliberately omitted
// from the user-facing per-repo onboarding catalog.
//   - /fullsend: backward-compat alias for the /fs-retro form; /fs-retro is the
//     primary command documented in the catalog.
var commandsNotInPerRepoCatalog = map[string]bool{
	"/fullsend": true,
}

// TestPerRepoOnboardingCatalog guards the per-repo install PR body's
// slash-command catalog against drift from dispatch.yml's routing, in both
// directions — the per-repo analogue of TestReconcileReposSlashCommandCatalog in
// the scaffold package (which guards the per-org onboarding catalog). Pinning
// both catalogs to the same source (dispatch.yml) keeps the two onboarding
// surfaces from diverging.
func TestPerRepoOnboardingCatalog(t *testing.T) {
	dispatch, err := scaffold.FullsendRepoFile(".github/workflows/dispatch.yml")
	require.NoError(t, err)
	dispatchStr := string(dispatch)

	// dispatch.yml routes on bare command tokens; the catalog renders them as
	// backtick-wrapped bullets (e.g. `/fs-triage`). Scope each pattern to its form
	// so a command name embedded in a docs URL is not mistaken for a command.
	dispatchCmdRE := regexp.MustCompile(`/fs-[a-z0-9-]+|/fullsend\b`)
	catalogCmdRE := regexp.MustCompile("`(/fs-[a-z0-9-]+|/fullsend)`")

	dispatchCmds := map[string]bool{}
	for _, cmd := range dispatchCmdRE.FindAllString(dispatchStr, -1) {
		dispatchCmds[cmd] = true
	}
	require.NotEmpty(t, dispatchCmds, "expected dispatch.yml to route on /fs-* commands")

	catalogCmds := map[string]bool{}
	for _, m := range catalogCmdRE.FindAllStringSubmatch(gettingStartedCatalog, -1) {
		catalogCmds[m[1]] = true
	}
	require.NotEmpty(t, catalogCmds, "expected the per-repo catalog to document /fs-* commands")

	// Forward: dispatch.yml commands must be documented (unless allow-listed).
	for cmd := range dispatchCmds {
		if commandsNotInPerRepoCatalog[cmd] {
			continue
		}
		assert.True(t, catalogCmds[cmd],
			"dispatch.yml routes on %s but the per-repo onboarding catalog does not document it "+
				"(add it to gettingStartedCatalog, or to commandsNotInPerRepoCatalog if intentional)", cmd)
	}

	// Reverse: cataloged commands must actually be routed by dispatch.yml (the
	// allow-list is applied here too so it stays symmetric with the forward check).
	for cmd := range catalogCmds {
		if commandsNotInPerRepoCatalog[cmd] {
			continue
		}
		assert.True(t, dispatchCmds[cmd],
			"per-repo onboarding catalog documents %s but dispatch.yml does not route on it", cmd)
	}
}

// TestFreshInstallBodyIncludesCatalog verifies the fresh-install PR body carries
// the Getting started catalog, so dropping the append is caught by CI.
func TestFreshInstallBodyIncludesCatalog(t *testing.T) {
	fc := forge.NewFakeClient()
	notInstalled := false
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &notInstalled})
	assert.Contains(t, meta.PRBody, "## Getting started")
	assert.Contains(t, meta.PRBody, "`/fs-triage`")
}

func TestRuntimeSection(t *testing.T) {
	t.Parallel()
	def := RuntimeSection("")
	assert.Contains(t, def, "## Runtime")
	assert.Contains(t, def, "run on **claude**")
	assert.Contains(t, RuntimeSection("pi"), "run on **pi**")
	assert.Contains(t, def, "`runtime:` in `.fullsend/config.yaml`")
	assert.Contains(t, def, "fullsend run --runtime")
	assert.Contains(t, def, "`agents:` entry")
	assert.True(t, strings.HasPrefix(def, "\n\n"), "section must be appended after the body with a paragraph break")
}
