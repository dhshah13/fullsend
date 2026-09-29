package runtime

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexDispatchFailureBlocksOnRealStderr(t *testing.T) {
	for _, tc := range []struct{ name, setup string }{
		{"input read failure", "def fail(*args): raise OSError('read failed')\njson.load = fail"},
		{"memory failure", "def fail(*args): raise MemoryError()\njson.load = fail"},
		{"interrupted policy", "def fail(*args): raise KeyboardInterrupt()\njson.load = fail"},
		{"unexpected exit", "def fail(*args): raise SystemExit(1)\njson.load = fail"},
		{"missing stderr wrapper", "sys.stderr = None"},
		{"closed stderr wrapper", "sys.stderr = open(os.devnull, 'w')\nsys.stderr.close()"},
		{"misdirected stderr wrapper", "sys.stderr = open(os.devnull, 'w')"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := json.Marshal(codexDispatchPy)
			require.NoError(t, err)
			program := "import json,os,sys\n" + tc.setup + "\nexec(" + string(source) + ")"
			cmd := exec.Command(pythonWithTomllib(t), "-I", "-c", program, `["default"]`)
			cmd.Stdin = strings.NewReader(`{"tool_name":"spawn_agent","tool_input":{"fork_context":true}}`)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			assert.Equal(t, 2, exit.ExitCode(), "other exit codes do not block native Codex")
			assert.NotEmpty(t, strings.TrimSpace(stderr.String()), "Codex requires the reason on the real stderr pipe")
			assert.Empty(t, stdout.String(), "a block is not a JSON allow response or a redirected stderr print")
		})
	}
}
