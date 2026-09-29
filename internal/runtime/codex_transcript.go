package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// The exec stream establishes which native threads must have published
// transcripts. Keep these identities outside the sandbox: a second listing
// cannot prove completeness after files disappear between usage and extraction.
var codexExpectedTranscripts sync.Map // sandbox name -> codexTranscriptIdentities

type codexTranscriptIdentities struct {
	RootID  string
	Threads []string
	Digests map[string]string
}

func rememberCodexTranscriptIdentities(sandboxName, rootID string, children map[string]bool) {
	identities := codexTranscriptIdentities{RootID: rootID}
	if rootID != "" {
		identities.Threads = append(identities.Threads, rootID)
	}
	for id := range children {
		if id != "" && id != rootID {
			identities.Threads = append(identities.Threads, id)
		}
	}
	slices.Sort(identities.Threads)
	codexExpectedTranscripts.Store(sandboxName, identities)
}

func clearCodexTranscriptIdentities(sandboxName string) {
	codexExpectedTranscripts.Delete(sandboxName)
}

// Capture only small integrity metadata while accounting reads the private
// files. Root exit and child close precede collection, so extraction must see
// the same complete bytes; matching session metadata alone is insufficient.
func rememberCodexTranscriptFile(sandboxName string, meta codexRolloutMeta, path string) error {
	expected, ok := codexExpectedTranscripts.Load(sandboxName)
	if !ok {
		return nil
	}
	identities := expected.(codexTranscriptIdentities)
	if !slices.Contains(identities.Threads, meta.ThreadID) {
		return nil
	}
	parent := identities.RootID
	if meta.ThreadID == identities.RootID {
		parent = ""
	}
	if meta.SessionID != identities.RootID || meta.ParentThreadID != parent {
		return fmt.Errorf("codex transcript identity contradicts observed thread %s", meta.ThreadID)
	}
	digest, err := codexTranscriptDigest(path)
	if err != nil {
		return err
	}
	if previous := identities.Digests[meta.ThreadID]; previous != "" && previous != digest {
		return fmt.Errorf("conflicting transcripts for observed codex thread %s", meta.ThreadID)
	}
	identities.Digests = maps.Clone(identities.Digests)
	if identities.Digests == nil {
		identities.Digests = make(map[string]string)
	}
	identities.Digests[meta.ThreadID] = digest
	codexExpectedTranscripts.Store(sandboxName, identities)
	return nil
}

func codexTranscriptDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, codexMaxArtifactBytes+1))
	if err != nil {
		return "", err
	}
	if n > codexMaxArtifactBytes {
		return "", fmt.Errorf("codex transcript exceeds integrity byte limit")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// downloadCodexRollouts streams complete, bounded, regular files to a private
// directory. Never download raw rollouts into the artifact directory: the
// download CLI can write the remote basename before renaming its destination.
func (r CodexRuntime) downloadCodexRollouts(ctx context.Context, sandboxName, privateDir string) ([]string, error) {
	remotePaths, err := r.listCodexRollouts(ctx, sandboxName)
	if err != nil {
		return nil, err
	}
	slices.Sort(remotePaths)
	remaining := int64(codexMaxArtifactBytes)
	paths := make([]string, 0, len(remotePaths))
	var failures []error
	for i, remote := range remotePaths {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		if err := codexValidSessionPath(r.codexSessionsDir(), remote); err != nil {
			failures = append(failures, err)
			continue
		}
		if remaining <= 0 {
			failures = append(failures, fmt.Errorf("codex rollouts exceed the collection byte limit"))
			break
		}
		local := filepath.Join(privateDir, fmt.Sprintf("%03d-%s", i, filepath.Base(remote)))
		n, err := codexDownloadRollout(ctx, sandboxName, r.codexSessionsDir(), remote, local, remaining)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		remaining -= n
		paths = append(paths, local)
	}
	return paths, errors.Join(failures...)
}

const codexListRolloutsPy = `import json,os,stat,sys
root=sys.argv[1]
if not stat.S_ISDIR(os.lstat(root).st_mode): raise ValueError('invalid sessions root')
paths=[]
size=0
def fail(error): raise error
for directory,dirs,files in os.walk(root,followlinks=False,onerror=fail):
 for name in files:
  path=os.path.join(directory,name)
  if name.endswith('.jsonl') and stat.S_ISREG(os.lstat(path).st_mode):
   paths.append(path)
   size+=len(path.encode('utf-8'))
   if len(paths)>128 or size>32768: raise ValueError('rollout listing exceeds bounds')
print(json.dumps(paths))
`

func (r CodexRuntime) listCodexRollouts(ctx context.Context, sandboxName string) ([]string, error) {
	python := "/usr/bin/python3"
	if held, ok := lookupRunnerHeldDigests(sandboxName); ok && held.Python != "" {
		python = held.Python
	}
	command := shellQuote(python) + " -I -c " + shellQuote(codexListRolloutsPy) + " " + shellQuote(r.codexSessionsDir()) + " # fullsend-codex-list"
	reader, process, cancel, err := sandbox.ExecStreamReader(ctx, sandboxName, command, 10*time.Second, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("listing codex rollouts: %w", err)
	}
	defer cancel()
	defer reader.Close()
	var output bytes.Buffer
	const limit = 65536
	n, readErr := io.Copy(&output, io.LimitReader(reader, limit+1))
	if n > limit || readErr != nil {
		cancel()
	}
	waitErr := process.Wait()
	if n > limit || readErr != nil || waitErr != nil {
		return nil, errors.Join(fmt.Errorf("listing codex rollouts failed or exceeded bounds"), readErr, waitErr)
	}
	var paths []string
	if err := json.Unmarshal(output.Bytes(), &paths); err != nil || len(paths) > 128 {
		return nil, fmt.Errorf("invalid or oversized codex rollout listing")
	}
	return paths, nil
}

const codexReadRolloutPy = `import os,stat,sys
root,path,limit=sys.argv[1],sys.argv[2],int(sys.argv[3])
relative=path.removeprefix(root+'/')
if relative==path or any(p in ('','.','..') for p in relative.split('/')): raise ValueError('invalid rollout path')
fd=os.open(root,os.O_PATH|os.O_DIRECTORY|os.O_NOFOLLOW)
parts=relative.split('/')
for part in parts[:-1]:
 nextfd=os.open(part,os.O_PATH|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
 os.close(fd); fd=nextfd
source=os.open(parts[-1],os.O_RDONLY|os.O_NONBLOCK|os.O_NOFOLLOW,dir_fd=fd)
os.close(fd)
info=os.fstat(source)
if not stat.S_ISREG(info.st_mode) or info.st_size>limit: raise ValueError('rollout is not a bounded regular file')
with os.fdopen(source,'rb') as stream:
 left=limit+1
 while left:
  chunk=stream.read(min(left,65536))
  if not chunk: break
  sys.stdout.buffer.write(chunk); left-=len(chunk)
`

// codexDownloadRollout returns the number of bytes written to local.
func codexDownloadRollout(ctx context.Context, sandboxName, root, remote, local string, limit int64) (int64, error) {
	python := "/usr/bin/python3"
	if held, ok := lookupRunnerHeldDigests(sandboxName); ok && held.Python != "" {
		python = held.Python
	}
	command := shellQuote(python) + " -I -c " + shellQuote(codexReadRolloutPy) + " " + shellQuote(root) + " " + shellQuote(remote) + fmt.Sprintf(" %d # fullsend-codex-rollout", limit)
	reader, process, cancel, err := sandbox.ExecStreamReader(ctx, sandboxName, command, 15*time.Second, io.Discard)
	if err != nil {
		return 0, err
	}
	defer cancel()
	defer reader.Close()
	f, err := os.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cancel()
		process.Wait()
		return 0, err
	}
	n, copyErr := io.Copy(f, io.LimitReader(reader, limit+1))
	closeErr := f.Close()
	if n > limit || copyErr != nil {
		cancel()
	}
	waitErr := process.Wait()
	if n > limit || copyErr != nil || closeErr != nil || waitErr != nil {
		os.Remove(local)
		return 0, errors.Join(fmt.Errorf("incomplete codex rollout transfer (%d bytes; limit %d)", n, limit), copyErr, closeErr, waitErr)
	}
	return n, nil
}

// ExtractTranscripts downloads codex's rollout session files (written under
// the runner-owned $CODEX_HOME/sessions/YYYY/MM/DD/, one per thread) into
// outputDir as <agentLabel>-<basename>, with the same path containment as the
// Claude and pi handlers.
//
// Only `.jsonl` is collected. codex writes the running session's rollout
// uncompressed and compresses older ones in place
// (codex-rs/thread-store/src/local/helpers.rs), so a `.jsonl.zst` is never the
// current iteration's transcript — and the sessions directory is
// agent-writable, so trusting that suffix meant a plaintext file *named*
// `x.jsonl.zst` shipped as an artifact that codexRedactFile declined to
// rewrite. Extension is not evidence of content: the suffix is excluded and
// every candidate's first line must parse as a codex rollout envelope before
// it is kept.
//
// ClearIterationArtifacts empties the sessions directory between iterations,
// so in practice this finds the current run's rollout.
func (r CodexRuntime) ExtractTranscripts(sandboxName, agentLabel, outputDir string) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrIncompleteEvidence, err)
		}
	}()
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("opening output root: %w", err)
	}
	defer root.Close()

	privateDir, err := os.MkdirTemp("", "fullsend-codex-rollouts-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(privateDir)
	// ponytail: a second transfer of what collectCodexUsage already read,
	// reconciled by digest; after a timeout these final bytes can hold usage it
	// never counted. Publish its copy instead if transfer cost matters.
	paths, collectionErr := r.downloadCodexRollouts(context.Background(), sandboxName, privateDir)
	if paths == nil && collectionErr != nil {
		return collectionErr
	}
	if collectionErr != nil {
		fmt.Fprintf(os.Stderr, "  [%s] Incomplete transcript collection: %s\n", agentLabel, sanitizeOutput(collectionErr.Error()))
	}
	failures := []error{collectionErr}
	published := make(map[string]codexRolloutMeta)
	var identities codexTranscriptIdentities
	if expected, ok := codexExpectedTranscripts.Load(sandboxName); ok {
		identities = expected.(codexTranscriptIdentities)
	}
	for _, path := range paths {
		if err := codexIsRolloutFile(path); err != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Discarded invalid rollout: %s\n", agentLabel, sanitizeOutput(err.Error()))
			failures = append(failures, err)
			continue
		}
		_, basename, _ := strings.Cut(filepath.Base(path), "-")
		localName := agentLabel + "-" + basename
		var head struct {
			Payload codexRolloutMeta `json:"payload"`
		}
		// Identity comes from native metadata, never the root's prose or a role
		// alone. Preserve legacy filenames when old rollouts lack identities.
		if f, openErr := os.Open(path); openErr == nil {
			decodeErr := json.NewDecoder(f).Decode(&head)
			f.Close()
			if _, idErr := uuid.Parse(head.Payload.ThreadID); decodeErr == nil && idErr == nil {
				localName = agentLabel + "-" + head.Payload.ThreadID + ".jsonl"
				if head.Payload.ParentThreadID != "" {
					role := head.Payload.Role
					if !config.ValidSubagentKey(role) {
						role = "generic"
					}
					localName = agentLabel + "-child-" + role + "-" + head.Payload.ThreadID + ".jsonl"
				}
			}
		}
		if expected := identities.Digests[head.Payload.ThreadID]; expected != "" {
			digest, digestErr := codexTranscriptDigest(path)
			if digestErr != nil || digest != expected {
				failures = append(failures, errors.Join(fmt.Errorf("codex transcript changed after usage collection for thread %s", head.Payload.ThreadID), digestErr))
				continue
			}
		}
		err := codexRedactFile(path)
		if err == nil {
			err = codexPublishTranscript(root, path, localName)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Cannot save transcript: %s\n", agentLabel, sanitizeOutput(err.Error()))
			failures = append(failures, err)
			continue
		}
		published[head.Payload.ThreadID] = head.Payload
		fmt.Fprintf(os.Stderr, "  [%s] Saved transcript: %s\n", agentLabel, localName)
	}
	for _, id := range identities.Threads {
		meta, exists := published[id]
		parent := identities.RootID
		if id == identities.RootID {
			parent = ""
		}
		if !exists || meta.SessionID != identities.RootID || meta.ParentThreadID != parent {
			failures = append(failures, fmt.Errorf("missing transcript for observed codex thread %s", id))
		}
	}
	return errors.Join(failures...)
}

// Only already-redacted bytes ever enter the public output directory. A
// temporary file plus root-relative rename makes publication atomic even when
// the private download directory is on a different filesystem.
func codexPublishTranscript(root *os.Root, source, name string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	stage := name + ".fullsend-staging"
	root.Remove(stage)
	out, err := root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(stage)
	_, copyErr := io.Copy(out, in)
	if err := errors.Join(copyErr, out.Close()); err != nil {
		return err
	}
	return root.Rename(stage, name)
}

// ExtractDebugLog downloads the stderr capture Run writes when debug is on.
// codex exec has no debug flag of its own: its tracing goes to stderr behind
// the RUST_LOG filter, and Run redirects that to this file.
//
// It gets the same pattern redaction as the other artifacts. codex logs at
// error level by default and raises to whatever RUST_LOG asks for, so this
// file carries request bodies, hook output and command text — the same
// material output.jsonl does, and it is uploaded the same way.
func (r CodexRuntime) ExtractDebugLog(sandboxName, localPath, debug string) error {
	if debug == "" {
		return nil
	}
	privateDir, err := os.MkdirTemp("", "fullsend-codex-debug-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(privateDir)
	privatePath := filepath.Join(privateDir, codexDebugLogFile)
	if _, err := codexDownloadRollout(context.Background(), sandboxName, r.WorkspaceDir(), r.WorkspaceDir()+"/"+codexDebugLogFile, privatePath, codexMaxArtifactBytes); err != nil {
		return err
	}
	if err := codexRedactTextFile(privatePath); err != nil {
		// Better no debug log than an unredacted one: it is a convenience
		// artifact, and the run does not depend on it.
		return fmt.Errorf("redacting %s: %w", codexDebugLogFile, err)
	}
	root, err := os.OpenRoot(filepath.Dir(localPath))
	if err != nil {
		return err
	}
	defer root.Close()
	return codexPublishTranscript(root, privatePath, filepath.Base(localPath))
}

// codexValidSessionPath reports whether a path `find` returned is safe to
// download: inside the sessions directory, and free of the control characters
// that would mean the listing was split wrong or crafted.
func codexValidSessionPath(sessionsDir, path string) error {
	if !strings.HasPrefix(path, sessionsDir+"/") {
		return fmt.Errorf("not under %s", sessionsDir)
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("contains a parent-directory segment")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("contains a control character")
		}
	}
	return nil
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
