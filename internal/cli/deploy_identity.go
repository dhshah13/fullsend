package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// revParse runs git rev-parse. Overridden in tests.
var revParse = gitRevParse

// resolveMintDeployCommit returns the commit to stamp into a mint deploy.
// When commit is already set to something other than "dev"/empty, it is
// returned unchanged. When deploying from on-disk source (sourceDir exists)
// and commit is unset, it tries git -C sourceDir rev-parse HEAD. Embedded
// deploys (missing sourceDir) never invoke git.
//
// If sourceDir exists but git resolution fails, the original commit is
// returned with a non-nil error so callers can warn without failing deploy.
func resolveMintDeployCommit(commit, sourceDir string) (string, error) {
	if commit != "" && commit != "dev" {
		return commit, nil
	}
	if sourceDir == "" {
		return commit, nil
	}
	info, err := os.Stat(sourceDir)
	if err != nil || !info.IsDir() {
		return commit, nil
	}
	sha, err := revParse(sourceDir, "HEAD")
	if err != nil {
		return commit, fmt.Errorf("git rev-parse HEAD in %s: %w", sourceDir, err)
	}
	if sha == "" {
		return commit, fmt.Errorf("git rev-parse HEAD in %s returned empty", sourceDir)
	}
	return sha, nil
}

// resolveAndReportMintDeployCommit resolves the deploy commit and prints
// warn/info messages for the operator. Always returns a stamp value
// (possibly still "dev"/empty on failure).
func resolveAndReportMintDeployCommit(printer *ui.Printer, commit, sourceDir string) string {
	deployCommit, resolveErr := resolveMintDeployCommit(commit, sourceDir)
	if resolveErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not resolve mint commit from checkout: %v", resolveErr))
	} else if deployCommit != commit && deployCommit != "" && deployCommit != "dev" {
		printer.StepInfo(fmt.Sprintf("Resolved mint commit from checkout: %s", deployCommit))
	}
	return deployCommit
}

func gitRevParse(dir string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", dir, "rev-parse"}, args...)
	cmd := exec.Command("git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}
