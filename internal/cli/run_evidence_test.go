package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type evidenceTestRuntime struct {
	agentruntime.DummyRuntime
	runErr     error
	extractErr error
	exitCode   int
	remoteDir  string
	runs       int
	extracted  bool
}

func (*evidenceTestRuntime) Bootstrap(agentruntime.BootstrapInput) error { return nil }

func (r *evidenceTestRuntime) Run(_ context.Context, params agentruntime.RunParams, _ *ui.Printer, _ time.Time, metrics *agentruntime.RunMetrics) (int, error) {
	r.runs++
	metrics.NumTurns = 2
	metrics.InputTokens = 17
	metrics.CostUnavailable = true
	if err := os.WriteFile(filepath.Join(r.remoteDir, "result.json"), []byte(`{"ok":true}`+"\n"), 0o600); err != nil {
		return -1, err
	}
	if err := os.WriteFile(params.OutputPath, []byte("{\"type\":\"turn.completed\"}\n"), 0o600); err != nil {
		return -1, err
	}
	return r.exitCode, r.runErr
}

func (r *evidenceTestRuntime) ExtractTranscripts(_, _ string, outputDir string) error {
	r.extracted = true
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "root.jsonl"), []byte("{\"preserved\":true}\n"), 0o600); err != nil {
		return err
	}
	return r.extractErr
}

func TestRunAgent_IncompleteEvidencePreservesArtifactsAndFailsValidationGate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		runErr     error
		extractErr error
		exitCode   int
		sweep      bool
		retry      bool
		wantErr    bool
	}{
		{name: "missing runtime evidence", runErr: fmt.Errorf("missing child: %w", agentruntime.ErrIncompleteEvidence), exitCode: 1, wantErr: true},
		{name: "missing evidence stops validation retries", runErr: agentruntime.ErrIncompleteEvidence, exitCode: 1, retry: true, wantErr: true},
		{name: "missing extracted evidence", extractErr: fmt.Errorf("missing child transcript: %w", agentruntime.ErrIncompleteEvidence), wantErr: true},
		{name: "sweep cannot erase missing evidence", extractErr: agentruntime.ErrIncompleteEvidence, sweep: true, wantErr: true},
		{name: "ordinary extraction warning remains nonfatal", extractErr: errors.New("optional transcript unavailable")},
		{name: "ordinary nonzero exit remains validation gated", exitCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			neutralizeAgentsRepoFallback(t)
			for _, key := range []string{"GITHUB_ACTIONS", "GITHUB_OUTPUT", "FULLSEND_MINT_URL", "FULLSEND_GCP_OIDC_URL", "ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "TRACEPARENT", "FULLSEND_RUNTIME", "FULLSEND_MODEL", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
				t.Setenv(key, "")
			}
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("FULLSEND_SANDBOX_ARCH", "amd64")
			dir, outputBase, repoDir, remoteDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("FULLSEND_EVIDENCE_TEST_REMOTE", remoteDir)
			if tc.sweep {
				t.Setenv("FULLSEND_EVIDENCE_TEST_FAIL_REPO", "1")
			} else {
				t.Setenv("FULLSEND_EVIDENCE_TEST_FAIL_REPO", "")
			}
			postMarker := filepath.Join(dir, "post-ran")
			validate := "#!/bin/sh\nset -eu\ntest -s \"$FULLSEND_OUTPUT_SCHEMA\"\ntest \"$(cat output/result.json)\" = '{\"ok\":true}'\necho 'schema fixture passed'\n"
			maxIterations := 1
			if tc.retry {
				validate += "exit 1\n"
				maxIterations = 2
			}
			files := map[string]string{
				"config.yaml":        "agents:\n  - harness/probe.yaml\n",
				"agents/probe.md":    "Return a valid result.\n",
				"schema.json":        `{"type":"object","required":["ok"],"properties":{"ok":{"const":true}},"additionalProperties":false}`,
				"validate.sh":        validate,
				"post.sh":            "#!/bin/sh\ntouch " + shellQuoteForTest(postMarker) + "\n",
				"harness/probe.yaml": fmt.Sprintf("agent: agents/probe.md\nrole: test\nsecurity:\n  enabled: false\npost_script: post.sh\nvalidation_loop:\n  max_iterations: %d\n  script: validate.sh\n  schema: schema.json\n", maxIterations),
			}
			for name, contents := range files {
				path := filepath.Join(dir, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(contents), 0o755))
			}
			// This ELF header only satisfies host-side upload validation. The
			// openshell stub never executes it or any sandbox command.
			elfHeader := make([]byte, 64)
			copy(elfHeader, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
			binary.LittleEndian.PutUint16(elfHeader[16:], 2)
			binary.LittleEndian.PutUint16(elfHeader[18:], 62)
			binary.LittleEndian.PutUint32(elfHeader[20:], 1)
			binary.LittleEndian.PutUint16(elfHeader[52:], 64)
			localBinary := filepath.Join(dir, "test-linux-binary")
			require.NoError(t, os.WriteFile(localBinary, elfHeader, 0o600))
			binDir := t.TempDir()
			stub := `#!/bin/sh
set -eu
case "$1 $2" in
  'gateway list') echo test-gateway ;;
  'sandbox get') echo 'Phase: Ready' ;;
  'sandbox exec')
    for command do :; done
    case "$command" in
      'find /sandbox/workspace/output -type f'*) echo /sandbox/workspace/output/result.json ;;
      *'echo NOTOKEN'*) echo NOTOKEN ;;
    esac ;;
  'sandbox download')
    case "$4" in
      /sandbox/workspace/output/result.json) mkdir -p "$5"; cp "$FULLSEND_EVIDENCE_TEST_REMOTE/result.json" "$5/result.json" ;;
      *) test -z "$FULLSEND_EVIDENCE_TEST_FAIL_REPO" || exit 1; mkdir -p "$5" ;;
    esac ;;
  'sandbox create'|'sandbox delete'|'sandbox upload'|'logs '*) ;;
  *) echo "unexpected openshell operation: $1 $2" >&2; exit 1 ;;
esac
`
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(stub), 0o755))
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			rt := &evidenceTestRuntime{runErr: tc.runErr, extractErr: tc.extractErr, exitCode: tc.exitCode, remoteDir: remoteDir}
			resolver := func(runOverrides, runConfig, string) (agentruntime.Backend, string, error) {
				return agentruntime.Backend{Runtime: rt, Transcripts: rt}, "test", nil
			}
			var out bytes.Buffer
			err := runAgentWithRuntimeResolver(context.Background(), "probe", dir, outputBase, repoDir, localBinary, nil, false, "", "", "", resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{}, resolver)
			if tc.wantErr {
				assert.ErrorIs(t, err, agentruntime.ErrIncompleteEvidence, out.String())
				assert.NoFileExists(t, postMarker, "incomplete evidence must suppress post-script side effects")
			} else {
				require.NoError(t, err, out.String())
				assert.FileExists(t, postMarker)
			}
			require.Equal(t, 1, rt.runs, "a retry cannot restore missing evidence: %s", out.String())
			assert.NotContains(t, out.String(), "Will retry")
			assert.True(t, rt.extracted, "runtime evidence errors must still collect available transcripts")
			assert.Contains(t, out.String(), "schema fixture passed", "the output passes validation independently of evidence completeness")
			runs, err := filepath.Glob(filepath.Join(outputBase, "fs-*"))
			require.NoError(t, err)
			require.Len(t, runs, 1)
			assert.FileExists(t, filepath.Join(runs[0], "iteration-1", "transcripts", "root.jsonl"))
			assert.FileExists(t, filepath.Join(runs[0], "iteration-1", "output", "result.json"))
			data, err := os.ReadFile(filepath.Join(runs[0], "metrics.json"))
			require.NoError(t, err)
			var metrics aggregateMetrics
			require.NoError(t, json.Unmarshal(data, &metrics))
			assert.Equal(t, 17, metrics.TokenUsage.Input)
			assert.True(t, metrics.CostUnavailable)
		})
	}
}
