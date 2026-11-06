package scaffold

import (
	"fmt"
	"regexp"
)

const (
	harnessBaseURLPrefix = "https://raw.githubusercontent.com/fullsend-ai/fullsend/"
	harnessURLPath       = "internal/scaffold/fullsend-repo/harness/"
)

var (
	validHarnessName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	validCommitSHA   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// HarnessBaseURL returns the raw.githubusercontent.com URL for a scaffold
// harness template at a specific commit SHA.
func HarnessBaseURL(harnessName, commitSHA string) (string, error) {
	if !validHarnessName.MatchString(harnessName) {
		return "", fmt.Errorf("invalid harness name %q: must match %s", harnessName, validHarnessName.String())
	}
	if !validCommitSHA.MatchString(commitSHA) {
		return "", fmt.Errorf("invalid commit SHA %q: must be a 40-character lowercase hex string", commitSHA)
	}
	return harnessBaseURLPrefix + commitSHA + "/" + harnessURLPath + harnessName + ".yaml", nil
}

// HarnessContent returns the raw YAML bytes of an embedded scaffold harness
// template. This is the same content served by raw.githubusercontent.com for
// the release commit the CLI was built from.
func HarnessContent(harnessName string) ([]byte, error) {
	if !validHarnessName.MatchString(harnessName) {
		return nil, fmt.Errorf("invalid harness name %q: must match %s", harnessName, validHarnessName.String())
	}
	data, err := content.ReadFile("fullsend-repo/harness/" + harnessName + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("unknown harness %q: %w", harnessName, err)
	}
	return data, nil
}
