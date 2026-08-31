package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// warnRepoSkillCollisions reports repo skills that Claude Code will ignore
// because harness skills are installed at the higher-precedence personal level.
func warnRepoSkillCollisions(repoDir string, harnessSkillDirs []string, printer *ui.Printer) {
	harnessSkills := make(map[string]struct{}, len(harnessSkillDirs))
	for _, skillDir := range harnessSkillDirs {
		if skillDir == "" {
			continue
		}
		if isReadableSkillMarker(skillDir) {
			harnessSkills[filepath.Base(skillDir)] = struct{}{}
		}
	}

	projectSkillsDir := filepath.Join(repoDir, ".claude", "skills")
	entries, err := os.ReadDir(projectSkillsDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		name := entry.Name()
		if _, collision := harnessSkills[name]; !collision {
			continue
		}
		if !isReadableSkillMarker(filepath.Join(projectSkillsDir, name)) {
			continue
		}
		printer.StepWarn(fmt.Sprintf(
			"Repo skill %q is shadowed by a harness skill of the same name; use a unique skill name to extend it, or use base: harness composition to override it",
			name,
		))
	}
}

func isReadableSkillMarker(skillDir string) bool {
	marker := filepath.Join(skillDir, "SKILL.md")
	info, err := os.Stat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	file, err := os.Open(marker)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}
