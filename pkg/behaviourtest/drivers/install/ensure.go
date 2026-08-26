package install

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

const (
	// settleMaxAttempts is how many times awaitWorkflowReady polls
	// GetWorkflow before giving up.
	settleMaxAttempts = 30

	// settlePoll is the delay between GetWorkflow polls.
	settlePoll = 5 * time.Second
)

// ensurer lazily creates and installs repos on demand for behaviour
// scenarios. Results are cached by org/repo key so that a second scenario
// leasing the same name within a suite run skips redundant work.
//
// This is an unexported interface used internally by composedDriver.
// The suite does not construct or reference it directly.
//
// Thread safety: EnsureRepo is safe for concurrent callers.
// A singleflight.Group serializes in-flight ensures per key so that
// concurrent first-calls for the same repo only perform create+install
// once; other callers wait and share the result.
type ensurer interface {
	// EnsureRepo guarantees org/repoName exists and has fullsend installed.
	// If the repo does not exist it is created (the forge's auto_init
	// provides the initial commit). If fullsend is not installed (per
	// post-install validation) it runs the per-repo install flow
	// (inference provision + github setup).
	EnsureRepo(ctx context.Context, org, repoName string) error
}

// SettleFunc is called after a repo is freshly created or installed to
// wait until GitHub Actions recognises the workflow file. The default
// implementation polls GetWorkflow; tests inject a no-op.
type SettleFunc func(ctx context.Context, client forge.Client, org, repo, workflowFile string, logf func(string, ...any)) error

type repoEnsurer struct {
	e2eCfg e2etest.EnvConfig
	client forge.Client
	token  string
	binary string
	logf   func(string, ...any)
	runCLI CLIRunnerFunc // injectable; defaults to e2etest.TryRunCLI
	settle SettleFunc    // injectable; defaults to awaitWorkflowReady

	mu       sync.Mutex
	ensured  map[string]struct{} // keyed by org/repo; only successful results cached
	inflight singleflight.Group
}

// newRepoEnsurer returns an ensurer backed by the given forge client
// and CLI binary. The ensurer shares the same credentials and
// configuration as the per-repo install driver.
func newRepoEnsurer(
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary string,
	logf func(string, ...any),
) ensurer {
	return &repoEnsurer{
		e2eCfg:  e2eCfg,
		client:  client,
		token:   token,
		binary:  binary,
		logf:    logf,
		runCLI:  e2etest.TryRunCLI,
		settle:  awaitWorkflowReady,
		ensured: make(map[string]struct{}),
	}
}

func (e *repoEnsurer) EnsureRepo(ctx context.Context, org, repoName string) error {
	key := org + "/" + repoName

	e.mu.Lock()
	if _, ok := e.ensured[key]; ok {
		e.mu.Unlock()
		e.logf("[ensure] %s already ensured this run, skipping", key)
		return nil
	}
	e.mu.Unlock()

	// singleflight deduplicates concurrent callers for the same key so
	// only one goroutine runs doEnsure; others wait and share the result.
	_, err, _ := e.inflight.Do(key, func() (any, error) {
		// Re-check the cache inside the flight — a prior flight may
		// have populated it before this one started.
		e.mu.Lock()
		if _, ok := e.ensured[key]; ok {
			e.mu.Unlock()
			return nil, nil
		}
		e.mu.Unlock()

		if err := e.doEnsure(ctx, org, repoName); err != nil {
			return nil, err
		}

		e.mu.Lock()
		e.ensured[key] = struct{}{}
		e.mu.Unlock()

		return nil, nil
	})

	return err
}

// doEnsure performs the actual create-if-missing + install work.
// It always re-vendors the CLI binary so that pool repos run the
// binary built from the current checkout. Without this, leased repos
// that pass post-install validation keep a stale vendored binary from
// a prior run, silently missing dispatch fixes on the current branch.
func (e *repoEnsurer) doEnsure(ctx context.Context, org, repoName string) error {
	target := org + "/" + repoName

	// Step 1: delete the existing repo to reset accumulated git history.
	// Pool repos grow to GB scale from repeated test runs; the pre-review
	// shallow-clone deepening step takes 12+ minutes fetching bloated
	// history. Deleting and recreating gives a clean single-commit repo.
	if err := e.resetRepo(ctx, org, repoName, target); err != nil {
		return err
	}

	// Step 2: create repo (needed after reset, or if it never existed).
	if err := e.ensureRepoExists(ctx, org, repoName, target); err != nil {
		return err
	}

	// Step 3: the repo is always freshly created (step 1 deleted any
	// prior version), so fullsend is never pre-installed. Run the full
	// install flow and settle for Actions readiness.
	e.logf("[ensure] %s needs install (fresh repo)", target)

	// Step 4: run github setup --vendor to install fullsend and push
	// the current binary.
	if err := e.installFullsend(ctx, org, repoName, target); err != nil {
		return err
	}
	if err := ValidatePerRepoPostInstall(ctx, e.client, org, repoName); err != nil {
		return fmt.Errorf("post-install validation for %s: %w", target, err)
	}

	// Step 5: wait for Actions to recognise the workflow file.
	if e.settle != nil {
		if err := e.settle(ctx, e.client, org, repoName, PerRepoTriageWorkflow, e.logf); err != nil {
			return fmt.Errorf("waiting for Actions readiness on %s: %w", target, err)
		}
	}

	return nil
}

// resetRepo deletes an existing repo to clear accumulated git history.
// Pool repos grow to gigabyte scale from repeated test runs; the
// pre-review shallow-clone deepening step takes 12+ minutes fetching
// the bloated history. A fresh repo starts with just the auto_init
// commit. No-op when the repo does not exist.
func (e *repoEnsurer) resetRepo(ctx context.Context, org, repoName, target string) error {
	_, err := e.client.GetRepo(ctx, org, repoName)
	if err != nil {
		if forge.IsNotFound(err) {
			e.logf("[ensure] %s does not exist, no history to reset", target)
			return nil
		}
		return fmt.Errorf("checking repo %s for reset: %w", target, err)
	}

	e.logf("[ensure] deleting %s to reset accumulated git history", target)
	if err := e.client.DeleteRepo(ctx, org, repoName); err != nil {
		if forge.IsNotFound(err) {
			return nil // race: deleted between check and delete
		}
		return fmt.Errorf("deleting repo %s for history reset: %w", target, err)
	}

	return nil
}

// ensureRepoExists creates the repo if it does not already exist.
// The forge's CreateRepo uses auto_init, so GitHub creates an initial
// commit with a README — no explicit seeding is needed.
// Idempotent: a repo that already exists is left untouched.
func (e *repoEnsurer) ensureRepoExists(ctx context.Context, org, repoName, target string) error {
	_, err := e.client.GetRepo(ctx, org, repoName)
	if err == nil {
		return nil // repo exists
	}
	if !forge.IsNotFound(err) {
		return fmt.Errorf("checking repo %s: %w", target, err)
	}

	e.logf("[ensure] creating %s (auto_init provides initial commit)", target)
	if _, createErr := e.client.CreateRepo(ctx, org, repoName, "Behaviour test repo", false); createErr != nil {
		return fmt.Errorf("creating repo %s: %w", target, createErr)
	}

	return nil
}

// installFullsend runs inference provision (when a GCP project is
// configured) and fullsend github setup for the target repo.
func (e *repoEnsurer) installFullsend(_ context.Context, _, _, target string) error {
	return common.RunGitHubSetup(e.binary, e.token, target, e.e2eCfg.MintURL, e.e2eCfg.GCPProjectID, e.runCLI, e.logf)
}

// awaitWorkflowReady polls the forge's GetWorkflow API until the given
// workflow file is visible and in "active" state, or until the attempt
// limit is exhausted. On newly created repos, GitHub Actions takes a
// variable amount of time to index committed workflow files; events
// dispatched before the workflow is indexed are silently dropped.
func awaitWorkflowReady(ctx context.Context, client forge.Client, org, repo, workflowFile string, logf func(string, ...any)) error {
	target := org + "/" + repo
	logf("[ensure] waiting for Actions to recognise %s on %s", workflowFile, target)

	for attempt := 1; attempt <= settleMaxAttempts; attempt++ {
		wf, err := client.GetWorkflow(ctx, org, repo, workflowFile)
		if err == nil && wf != nil {
			logf("[ensure] %s visible on %s after %d attempt(s) (state=%s)", workflowFile, target, attempt, wf.State)
			return nil
		}

		if attempt < settleMaxAttempts {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while waiting for %s on %s: %w", workflowFile, target, ctx.Err())
			case <-time.After(settlePoll):
			}
		}
	}

	return fmt.Errorf("workflow %s not visible on %s after %d attempts", workflowFile, target, settleMaxAttempts)
}
