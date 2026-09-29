package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexTranscriptIntegrity_RejectedCopyPreservesFirstEvidence(t *testing.T) {
	// A failed second collection must neither replace nor erase the digest
	// that extraction uses to authenticate the first, complete transcript.
	const body = `{"type":"session_meta","payload":{"id":"01a0e868-0083-7571-b0e2-de21c3a6c910","session_id":"01a0e868-0083-7571-b0e2-de21c3a6c910","note":"original evidence"}}` + "\n"
	for _, problem := range []string{"missing file", "unreadable contents", "conflicting copy"} {
		t.Run(problem, func(t *testing.T) {
			sandboxName := t.Name()
			rememberCodexTranscriptIdentities(sandboxName, recordedRoot0158, nil)
			t.Cleanup(func() { clearCodexTranscriptIdentities(sandboxName) })
			meta := codexRolloutMeta{ThreadID: recordedRoot0158, SessionID: recordedRoot0158}
			original := filepath.Join(t.TempDir(), "original.jsonl")
			require.NoError(t, os.WriteFile(original, []byte(body), 0o600))
			require.NoError(t, rememberCodexTranscriptFile(sandboxName, meta, original))

			candidate := filepath.Join(t.TempDir(), "candidate.jsonl")
			switch problem {
			case "unreadable contents":
				// Opening a directory succeeds, but hashing its bytes must fail.
				require.NoError(t, os.Mkdir(candidate, 0o700))
			case "conflicting copy":
				require.NoError(t, os.WriteFile(candidate, []byte(strings.ReplaceAll(body, "original evidence", "substituted evidence")), 0o600))
			}
			err := rememberCodexTranscriptFile(sandboxName, meta, candidate)
			require.Error(t, err)
			if problem == "missing file" {
				assert.ErrorIs(t, err, os.ErrNotExist)
			} else if problem == "conflicting copy" {
				assert.ErrorContains(t, err, "conflicting transcripts")
			} else {
				var pathErr *os.PathError
				assert.ErrorAs(t, err, &pathErr)
			}

			fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "", map[string]string{"root.jsonl": body})
			out := t.TempDir()
			require.NoError(t, (CodexRuntime{}).ExtractTranscripts(sandboxName, "review", out))
			assert.JSONEq(t, body, readFileString(t, filepath.Join(out, "review-01a0e868-0083-7571-b0e2-de21c3a6c910.jsonl")))

			// Acceptance alone could also mean the digest was forgotten. The
			// same native identity with different bytes must still be rejected.
			changed := strings.ReplaceAll(body, "original evidence", "changed after collection")
			fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "changed.log"), t.TempDir(), "", map[string]string{"root.jsonl": changed})
			changedOut := t.TempDir()
			err = (CodexRuntime{}).ExtractTranscripts(sandboxName, "review", changedOut)
			require.ErrorIs(t, err, ErrIncompleteEvidence)
			entries, err := os.ReadDir(changedOut)
			require.NoError(t, err)
			assert.Empty(t, entries, "retained authentication must prevent publishing changed evidence")
		})
	}
}

func TestCodexTranscriptIntegrity_RefusesDigestPastArtifactLimit(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "oversized.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	// A sparse file exercises the bounded streaming hash without allocating
	// an artifact-sized string or buffer in the test process.
	require.NoError(t, f.Truncate(codexMaxArtifactBytes+1))
	require.NoError(t, f.Close())
	digest, err := codexTranscriptDigest(path)
	require.ErrorContains(t, err, "integrity byte limit")
	assert.Empty(t, digest, "a prefix hash must not authenticate a truncated artifact")
}

func TestCodexTranscriptIntegrity_DownloadPreservesExistingDestination(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing file", true: "symlink"}[linked], func(t *testing.T) {
			fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.158.0")
			previous := filepath.Join(t.TempDir(), "existing.jsonl")
			require.NoError(t, os.WriteFile(previous, []byte("previous private evidence"), 0o600))
			local := previous
			if linked {
				local = filepath.Join(t.TempDir(), "download.jsonl")
				require.NoError(t, os.Symlink(previous, local))
			}
			_, err := codexDownloadRollout(t.Context(), t.Name(), "/sessions", "/sessions/root.jsonl", local, 1024)
			require.ErrorIs(t, err, os.ErrExist)
			assert.Equal(t, "previous private evidence", readFileString(t, previous))
			if linked {
				target, err := os.Readlink(local)
				require.NoError(t, err)
				assert.Equal(t, previous, target, "failed download must not remove an existing link")
			}
		})
	}
}

func TestCodexTranscriptIntegrity_DownloadStartFailureCreatesNoFile(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	local := filepath.Join(t.TempDir(), "download.jsonl")
	_, err := codexDownloadRollout(t.Context(), t.Name(), "/sessions", "/sessions/root.jsonl", local, 1024)
	require.Error(t, err)
	_, err = os.Stat(local)
	assert.ErrorIs(t, err, os.ErrNotExist, "an unreachable sandbox must not leave an empty transcript")
}

func TestCodexTranscriptIntegrity_PrivateStorageFailureIsReported(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(map[bool]string{false: "transcripts", true: "debug log"}[debug], func(t *testing.T) {
			out := t.TempDir()
			blocked := filepath.Join(t.TempDir(), "not-a-directory")
			require.NoError(t, os.WriteFile(blocked, []byte("keep"), 0o600))
			t.Setenv("TMPDIR", blocked)
			var err error
			if debug {
				err = (CodexRuntime{}).ExtractDebugLog(t.Name(), filepath.Join(out, "debug.log"), "1")
			} else {
				err = (CodexRuntime{}).ExtractTranscripts(t.Name(), "review", out)
				require.ErrorIs(t, err, ErrIncompleteEvidence)
			}
			var pathErr *os.PathError
			require.ErrorAs(t, err, &pathErr)
			assert.Equal(t, "mkdir", pathErr.Op)
			assert.True(t, strings.HasPrefix(pathErr.Path, blocked+string(os.PathSeparator)))
			entries, err := os.ReadDir(out)
			require.NoError(t, err)
			assert.Empty(t, entries, "raw bytes must not fall back to public artifact storage")
		})
	}
}

func TestCodexTranscriptIntegrity_PublishFailureRetainsSafeSibling(t *testing.T) {
	fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "", map[string]string{
		"blocked.jsonl": `{"type":"session_meta","payload":{}}` + "\n",
		"safe.jsonl":    `{"type":"session_meta","payload":{"note":"kept","token":"` + codexTestSecret + `"}}` + "\n",
	})
	out := t.TempDir()
	blocked := filepath.Join(out, "review-blocked.jsonl")
	require.NoError(t, os.Mkdir(blocked, 0o700))
	marker := filepath.Join(blocked, "previous")
	require.NoError(t, os.WriteFile(marker, []byte("retain"), 0o600))
	err := (CodexRuntime{}).ExtractTranscripts(t.Name(), "review", out)
	require.ErrorIs(t, err, ErrIncompleteEvidence)
	assert.Equal(t, "retain", readFileString(t, marker))
	got := readFileString(t, filepath.Join(out, "review-safe.jsonl"))
	assert.NotContains(t, got, codexTestSecret)
	assert.JSONEq(t, `{"type":"session_meta","payload":{"note":"kept","token":"ghp_..."}}`, got)
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	require.Len(t, entries, 2, "failed publication must remove its staging file")
}

func TestCodexTranscriptIntegrity_PublishFailurePreservesPreviousBytes(t *testing.T) {
	for _, problem := range []string{"blocked staging path", "source read failure"} {
		t.Run(problem, func(t *testing.T) {
			out := t.TempDir()
			root, err := os.OpenRoot(out)
			require.NoError(t, err)
			defer root.Close()
			previous := filepath.Join(out, "root.jsonl")
			require.NoError(t, os.WriteFile(previous, []byte("previous redacted evidence"), 0o600))
			source := t.TempDir()
			stage := filepath.Join(out, "root.jsonl.fullsend-staging")
			if problem == "blocked staging path" {
				source = filepath.Join(source, "source.jsonl")
				require.NoError(t, os.WriteFile(source, []byte("replacement"), 0o600))
				require.NoError(t, os.Mkdir(stage, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(stage, "marker"), []byte("keep"), 0o600))
			}
			require.Error(t, codexPublishTranscript(root, source, "root.jsonl"))
			assert.Equal(t, "previous redacted evidence", readFileString(t, previous))
			if problem == "blocked staging path" {
				assert.Equal(t, "keep", readFileString(t, filepath.Join(stage, "marker")))
			} else {
				_, err := os.Stat(stage)
				assert.ErrorIs(t, err, os.ErrNotExist, "a failed copy must not leave a public partial artifact")
			}
		})
	}
}

func TestCodexTranscriptIntegrity_DebugPublicationFailureIsReported(t *testing.T) {
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.158.0")
	t.Setenv("FULLSEND_TEST_DOWNLOAD_BODY", "authorization: Bearer "+codexTestSecret)
	out := t.TempDir()
	err := (CodexRuntime{}).ExtractDebugLog(t.Name(), filepath.Join(out, "missing", "debug.log"), "1")
	require.ErrorIs(t, err, os.ErrNotExist)
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	assert.Empty(t, entries, "a publication failure must not expose the raw log")
}

func TestCodexTranscriptIntegrity_CollectionStopsAtAggregateLimit(t *testing.T) {
	root := (CodexRuntime{}).codexSessionsDir()
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.158.0", "", root+"/a.jsonl\n"+root+"/b.jsonl")
	fake, err := exec.LookPath("openshell")
	require.NoError(t, err)
	require.NoError(t, os.Rename(fake, fake+"-base"))
	large := filepath.Join(t.TempDir(), "large.jsonl")
	f, err := os.Create(large)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(codexMaxArtifactBytes))
	require.NoError(t, f.Close())
	secondStarted := filepath.Join(t.TempDir(), "second-transfer-started")
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		" *'/a.jsonl'*fullsend-codex-rollout*) exec cat " + shellQuote(large) + ";;\n" +
		" *'/b.jsonl'*fullsend-codex-rollout*) touch " + shellQuote(secondStarted) + "; printf 'second'; exit 0;;\n" +
		"esac\nexec " + shellQuote(fake+"-base") + " \"$@\"\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o755))
	paths, err := (CodexRuntime{}).downloadCodexRollouts(t.Context(), t.Name(), t.TempDir())
	require.ErrorContains(t, err, "collection byte limit")
	require.Len(t, paths, 1, "keep the complete first file even when collection cannot continue")
	info, err := os.Stat(paths[0])
	require.NoError(t, err)
	assert.EqualValues(t, codexMaxArtifactBytes, info.Size())
	_, err = os.Stat(secondStarted)
	assert.ErrorIs(t, err, os.ErrNotExist, "do not start another remote transfer when no byte budget remains")
}
