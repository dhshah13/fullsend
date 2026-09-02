package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Artifact-side redaction for codex runs.
//
// The sandbox hook chain protects the *model's context*: on codex a PostToolUse
// block withholds the tool result from the model, and a sanitizer's rewrite is
// dropped because codex accepts no output rewrite for built-in tools. Neither
// touches what codex writes down. The `exec --json` stream keeps each command's
// raw `aggregated_output`, and the rollout session file keeps the same tool
// output, so a credential the chain masked for the model would still land in
// `output.jsonl` and `transcripts/` — both of which are uploaded as run
// artifacts.
//
// Claude Code does not have this gap: its stream carries the post-hook result.
// So both codex artifacts are filtered here, through the same
// security.SecretRedactor the other runtimes' progress parsers use — the shared
// pattern list, not a second copy of it (fullsend#6920).
//
// This is pattern redaction, not the hook chain: it masks credential-shaped
// values and the exact runtime secrets the runner registered. It does not
// withhold a canary, and it does not condense or normalize. The security matrix
// states the difference.

// codexRedactMaxLine bounds the line buffer. A codex JSONL line is a single
// event; `aggregated_output` is already truncated by codex's own output policy,
// so anything past this is a stream that never emits a newline, and buffering
// it forever would be a memory leak on a hostile stream.
const codexRedactMaxLine = 8 << 20

// codexRedactingWriter redacts a JSONL stream line by line as it is written.
// Partial lines are buffered until their newline arrives; Flush handles a
// stream that ends without one.
type codexRedactingWriter struct {
	w   io.Writer
	buf bytes.Buffer
}

func newCodexRedactingWriter(w io.Writer) *codexRedactingWriter {
	return &codexRedactingWriter{w: w}
}

// Write always reports the full input as consumed: it is the tee side of an
// io.TeeReader, and reporting a short write there would abort the read of a
// stream that is otherwise fine.
func (c *codexRedactingWriter) Write(p []byte) (int, error) {
	c.buf.Write(p)
	for {
		i := bytes.IndexByte(c.buf.Bytes(), '\n')
		if i < 0 {
			if c.buf.Len() > codexRedactMaxLine {
				// No newline in sight: flush what we have as text rather than
				// grow without bound.
				if err := c.flushBuffer(); err != nil {
					return len(p), err
				}
			}
			return len(p), nil
		}
		line := c.buf.Next(i + 1)
		if _, err := c.w.Write(append(codexRedactJSONLine(line[:i]), '\n')); err != nil {
			return len(p), err
		}
	}
}

// Flush writes any buffered partial line. Run calls it before the tee file is
// closed so a stream cut mid-line still leaves a redacted tail.
func (c *codexRedactingWriter) Flush() error {
	if c.buf.Len() == 0 {
		return nil
	}
	return c.flushBuffer()
}

func (c *codexRedactingWriter) flushBuffer() error {
	rest := c.buf.Next(c.buf.Len())
	_, err := c.w.Write(codexRedactJSONLine(rest))
	return err
}

// codexRedactJSONLine redacts every string value in one JSONL line, keys
// excluded. A line that is not JSON is redacted as plain text rather than
// passed through: an unparseable line is exactly where a truncated or hostile
// payload would sit.
func codexRedactJSONLine(line []byte) []byte {
	if len(bytes.TrimSpace(line)) == 0 {
		return line
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(line))
	// Numbers stay verbatim: without this an exit_code or a token count would
	// round-trip through float64 and could come back in exponent form.
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return []byte(redactSummary(string(line)))
	}
	out, err := json.Marshal(codexRedactValue(v))
	if err != nil {
		return []byte(redactSummary(string(line)))
	}
	return out
}

// codexRedactValue walks a decoded JSON value and redacts its strings.
func codexRedactValue(v any) any {
	switch t := v.(type) {
	case string:
		return redactSummary(t)
	case []any:
		for i := range t {
			t[i] = codexRedactValue(t[i])
		}
		return t
	case map[string]any:
		for k, val := range t {
			t[k] = codexRedactValue(val)
		}
		return t
	default:
		// json.Number, bool, nil.
		return v
	}
}

// codexRedactFile rewrites a downloaded JSONL artifact in place with every
// string value redacted. Used on the rollout session files, which carry the
// same raw tool output the stream does.
//
// A compressed rollout (`.jsonl.zst`) is left alone and reported: codex
// compresses older sessions in place, and decompressing one here to redact it
// would mean shipping a zstd decoder for an artifact the current iteration did
// not write. ClearIterationArtifacts empties the sessions directory between
// iterations, so a compressed file is not the run's own transcript.
func codexRedactFile(path string) error {
	if strings.HasSuffix(path, ".zst") {
		return fmt.Errorf("cannot redact compressed transcript %s", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	out.Grow(len(data))
	for rest := data; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			out.Write(codexRedactJSONLine(rest))
			break
		}
		out.Write(codexRedactJSONLine(rest[:i]))
		out.WriteByte('\n')
		rest = rest[i+1:]
	}
	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, out.Bytes(), mode)
}
