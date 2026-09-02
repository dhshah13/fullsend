package runtime

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// ExtractTranscripts downloads codex's rollout session files (written under
// the runner-owned $CODEX_HOME/sessions/YYYY/MM/DD/, one per thread) into
// outputDir as <agentLabel>-<basename>, with the same path containment as the
// Claude and pi handlers.
//
// Both `.jsonl` and `.jsonl.zst` are collected: codex compresses older
// rollouts in place (codex-rs/thread-store/src/local/helpers.rs), and while a
// single iteration's own file is written uncompressed, a kept sandbox running
// several iterations can carry either.
//
// ClearIterationArtifacts empties the sessions directory between iterations,
// so in practice this finds the current run's rollout; taking everything
// present is deliberate, because a file left behind is evidence, not noise.
func (r CodexRuntime) ExtractTranscripts(sandboxName, agentLabel, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("opening output root: %w", err)
	}
	defer root.Close()

	stdout, _, _, err := sandbox.Exec(sandboxName,
		fmt.Sprintf("find %s \\( -name '*.jsonl' -o -name '*.jsonl.zst' \\) 2>/dev/null || true",
			shellQuote(r.codexSessionsDir())),
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("finding transcripts: %w", err)
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		fmt.Fprintf(os.Stderr, "  [%s] No transcripts found\n", agentLabel)
		return nil
	}
	for _, remotePath := range strings.Split(trimmed, "\n") {
		remotePath = strings.TrimSpace(remotePath)
		if remotePath == "" {
			continue
		}
		localName := fmt.Sprintf("%s-%s", agentLabel, filepath.Base(remotePath))
		f, createErr := root.Create(localName)
		if createErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Skipping (path rejected): %s: %v\n", agentLabel, localName, createErr)
			continue
		}
		f.Close()
		localPath := filepath.Join(outputDir, localName)
		os.Remove(localPath)
		if dlErr := sandbox.DownloadFile(sandboxName, remotePath, localPath); dlErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Failed to copy transcript: %v\n", agentLabel, dlErr)
			continue
		}
		// The rollout carries the same raw tool output the stream does, and it
		// is uploaded as a run artifact, so it gets the same pattern redaction
		// (codex_redact.go). A rewrite that fails leaves the file in place and
		// says so rather than dropping the transcript.
		if redErr := codexRedactFile(localPath); redErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] WARNING: transcript %s was not redacted: %v\n",
				agentLabel, localName, redErr)
		}
		fmt.Fprintf(os.Stderr, "  [%s] Saved transcript: %s\n", agentLabel, localName)
	}
	return nil
}

// ExtractDebugLog downloads the stderr capture Run writes when debug is on.
// codex exec has no debug flag of its own: its tracing goes to stderr behind
// the RUST_LOG filter, and Run redirects that to this file.
func (r CodexRuntime) ExtractDebugLog(sandboxName, localPath, debug string) error {
	if debug == "" {
		return nil
	}
	return sandbox.DownloadFile(sandboxName, r.WorkspaceDir()+"/"+codexDebugLogFile, localPath)
}

// ParseTranscriptErrors scans every JSONL file in transcriptDir and reports
// those whose run ended in error.
//
// Only the tee'd `exec --json` capture (output.jsonl) yields a verdict:
// codex's rollout session files are a different envelope, which
// parseCodexTranscriptFile recognises and skips rather than misreading. That
// is the same division pi has — the stream capture is the runner's exit-code
// override input — with the difference that pi can also judge its session
// files. Classifying a rollout is tracked for a follow-up; the run's verdict
// does not depend on it, because Run already returns 1 on a stream-reported
// error.
func (CodexRuntime) ParseTranscriptErrors(transcriptDir string) []TranscriptError {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return nil
	}
	var summaries []TranscriptError
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if te, ok := parseCodexTranscriptFile(filepath.Join(transcriptDir, entry.Name())); ok && te.IsError {
			summaries = append(summaries, te)
		}
	}
	return summaries
}

// ParseTranscriptFile is the runner's exit-0 override input: the tee'd
// `exec --json` stream.
func (CodexRuntime) ParseTranscriptFile(path string) (TranscriptError, bool) {
	return parseCodexTranscriptFile(path)
}

func (CodexRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}
