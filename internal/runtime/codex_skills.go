package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

type codexSkillUpload struct {
	path string
	name string
}

// codexSkillUploads preserves declared skill names when several forge-specific
// skill directories share a basename. Codex discovers nested skills recursively,
// so a selected descendant is already delivered by its parent's directory upload.
func codexSkillUploads(skillDirs []string) ([]codexSkillUpload, error) {
	var skills []codexSkillUpload
	seen := make(map[string]string)
	for _, dir := range skillDirs {
		if dir == "" {
			continue
		}
		name := resolveSkillDisplayName(dir)
		if !isValidSkillName(name) || strings.HasPrefix(name, ".") {
			return nil, fmt.Errorf("invalid codex skill name %q from %q: expected a non-hidden single directory name", name, dir)
		}
		path, err := filepath.EvalSymlinks(dir)
		if err == nil {
			path, err = filepath.Abs(path)
		}
		if err != nil {
			return nil, fmt.Errorf("resolving codex skill %q: %w", dir, err)
		}
		if prior, ok := seen[name]; ok {
			if prior != path {
				return nil, fmt.Errorf("two codex skill paths both declare %q: %q and %q", name, prior, path)
			}
			continue
		}
		seen[name] = path
		skills = append(skills, codexSkillUpload{path: path, name: name})
	}
	var uploads []codexSkillUpload
	for _, candidate := range skills {
		nested := false
		for _, parent := range skills {
			rel, err := filepath.Rel(parent.path, candidate.path)
			if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				nested = true
				break
			}
		}
		if !nested {
			uploads = append(uploads, candidate)
		}
	}
	return uploads, nil
}

func codexUploadSkills(sandboxName, configDir string, skillDirs []string) error {
	uploads, err := codexSkillUploads(skillDirs)
	if err != nil {
		return err
	}
	for _, skill := range uploads {
		if err := sandbox.UploadDir(sandboxName, skill.path, configDir+"/skills/"+skill.name); err != nil {
			return fmt.Errorf("copying codex skill %q: %w", skill.path, err)
		}
		fmt.Fprintf(os.Stderr, "Skill %q: uploaded to sandbox\n", skill.name)
	}
	return nil
}
