package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codexTestSkill(t *testing.T, root, path, name string) string {
	t.Helper()
	dir := filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := "body\n"
	if name != "" {
		content = fmt.Sprintf("---\nname: %q\ndescription: Test skill.\n---\nbody\n", name)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
	canonical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return canonical
}

func TestCodexSkillUploads_ForgeNamesAndNestedSkills(t *testing.T) {
	root := t.TempDir()
	parent := codexTestSkill(t, root, "pr-review", "pr-review")
	child := codexTestSkill(t, root, "pr-review/github", "pr-review-github")
	labels := codexTestSkill(t, root, "issue-labels/github", "issue-labels")
	fallback := codexTestSkill(t, root, "local-skill", "")
	// A sibling whose prefix matches the parent must not be treated as nested.
	sibling := codexTestSkill(t, root, "pr-review-extra", "extra")
	for _, dirs := range [][]string{
		{child, "", labels, parent, fallback, sibling, parent},
		{parent, labels, child, fallback, sibling},
	} {
		uploads, err := codexSkillUploads(dirs)
		require.NoError(t, err)
		byName := make(map[string]string)
		for _, upload := range uploads {
			byName[upload.name] = upload.path
		}
		assert.Equal(t, map[string]string{"pr-review": parent, "issue-labels": labels, "local-skill": fallback, "extra": sibling}, byName)
	}
}

func TestCodexSkillUploads_DistinctSameBasename(t *testing.T) {
	root := t.TempDir()
	a := codexTestSkill(t, root, "review/github", "pr-review-github")
	b := codexTestSkill(t, root, "retro/github", "retro-analysis-github")
	uploads, err := codexSkillUploads([]string{a, b})
	require.NoError(t, err)
	assert.Equal(t, []codexSkillUpload{{a, "pr-review-github"}, {b, "retro-analysis-github"}}, uploads)
}

func TestCodexSkillUploads_RejectsDistinctDeclaredNameCollision(t *testing.T) {
	root := t.TempDir()
	a := codexTestSkill(t, root, "first", "shared")
	b := codexTestSkill(t, root, "second", "shared")
	_, err := codexSkillUploads([]string{a, b})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `both declare "shared"`)
}

func TestCodexSkillUploads_RejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"../config", "a/b", `a\b`, ".", "..", ".hidden", "a b", "a\nb", "/tmp/skill", "x;touch"} {
		t.Run(name, func(t *testing.T) {
			dir := codexTestSkill(t, t.TempDir(), "skill", name)
			_, err := codexSkillUploads([]string{dir})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid codex skill name")
		})
	}
}

func TestCodexSkillUploads_SymlinkAliasesAndMissingPath(t *testing.T) {
	root := t.TempDir()
	dir := codexTestSkill(t, root, "real", "declared")
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(dir, alias))
	uploads, err := codexSkillUploads([]string{alias, dir})
	require.NoError(t, err)
	assert.Equal(t, []codexSkillUpload{{dir, "declared"}}, uploads)
	_, err = codexSkillUploads([]string{filepath.Join(root, "missing")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving codex skill")
}

func TestCodexUploadSkills_UsesDeclaredDestinationWithoutNestedCopy(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.157.0")
	parent := codexTestSkill(t, root, "pr-review", "pr-review")
	child := codexTestSkill(t, root, "pr-review/github", "pr-review-github")
	labels := codexTestSkill(t, root, "issue-labels/github", "issue-labels")
	require.NoError(t, codexUploadSkills("sb", "/sandbox/codex-config", []string{child, parent, labels}))
	log := readFileString(t, logPath)
	assert.Contains(t, log, "rm -rf '/sandbox/codex-config/skills/pr-review'")
	assert.Contains(t, log, "rm -rf '/sandbox/codex-config/skills/issue-labels'")
	assert.NotContains(t, log, "skills/github")
	assert.NotContains(t, log, "skills/pr-review-github")
	assert.Equal(t, 2, strings.Count(log, "tar -xzf"))
}

func TestCodexUploadSkills_ReportsErrorsBeforeOrDuringTransfer(t *testing.T) {
	root := t.TempDir()
	dir := codexTestSkill(t, root, "skill", "skill")
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.157.0")
	err := codexUploadSkills("sb", "/sandbox/codex-config", []string{filepath.Join(root, "missing")})
	require.Error(t, err)
	_, statErr := os.Stat(logPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "invalid plan must not start uploading")
	t.Setenv("FULLSEND_TEST_FAIL_MATCH", "sandbox upload sb")
	err = codexUploadSkills("sb", "/sandbox/codex-config", []string{dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copying codex skill")
}

func TestCodexBootstrapRejectsInvalidSkillName(t *testing.T) {
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.157.0")
	skill := filepath.Join(t.TempDir(), ".hidden")
	require.NoError(t, os.Mkdir(skill, 0o755))
	err := (CodexRuntime{}).Bootstrap(bootstrapInput{sandboxName: "bad-skill", agentPath: writeAgentFile(t, codexTestAgentDef), skillDirs: []string{skill}})
	require.ErrorContains(t, err, "invalid codex skill name")
}
