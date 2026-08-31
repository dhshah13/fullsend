package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestWarnRepoSkillCollisions_WarnsForShadowedSkill(t *testing.T) {
	repoDir := t.TempDir()
	repoSkillDir := filepath.Join(repoDir, ".claude", "skills", "code-review")
	require.NoError(t, os.MkdirAll(repoSkillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoSkillDir, "SKILL.md"), []byte("# Repo review"), 0o644))

	harnessSkillDir := filepath.Join(t.TempDir(), "code-review")
	require.NoError(t, os.MkdirAll(harnessSkillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessSkillDir, "SKILL.md"), []byte("# Harness review"), 0o644))

	var output bytes.Buffer
	warnRepoSkillCollisions(repoDir, []string{harnessSkillDir}, ui.New(&output))

	assert.Contains(t, output.String(), `Repo skill "code-review" is shadowed by a harness skill of the same name`)
	assert.Contains(t, output.String(), "use a unique skill name to extend it")
	assert.Contains(t, output.String(), "base: harness composition to override it")
}

func TestWarnRepoSkillCollisions_DoesNotWarnWithoutCollision(t *testing.T) {
	repoDir := t.TempDir()
	projectSkillsDir := filepath.Join(repoDir, ".claude", "skills")
	require.NoError(t, os.MkdirAll(filepath.Join(projectSkillsDir, "repo-only"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectSkillsDir, "repo-only", "SKILL.md"), []byte("# Repo only"), 0o644))
	// A matching harness directory without SKILL.md is not a discoverable skill.
	require.NoError(t, os.MkdirAll(filepath.Join(projectSkillsDir, "markerless-harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectSkillsDir, "markerless-harness", "SKILL.md"), []byte("# Repo skill"), 0o644))
	// A matching repository directory without SKILL.md is not a discoverable skill.
	require.NoError(t, os.MkdirAll(filepath.Join(projectSkillsDir, "markerless-repo"), 0o755))

	harnessRoot := t.TempDir()
	harnessSkills := []string{
		filepath.Join(harnessRoot, "harness-only"),
		filepath.Join(harnessRoot, "markerless-harness"),
		filepath.Join(harnessRoot, "markerless-repo"),
	}

	var output bytes.Buffer
	warnRepoSkillCollisions(repoDir, harnessSkills, ui.New(&output))

	assert.Empty(t, output.String())
}
