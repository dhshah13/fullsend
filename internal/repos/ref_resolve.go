package repos

import (
	"context"
	"fmt"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// RefResolver resolves symbolic fullsend_ref values (tags, branches) to
// commit SHAs on the fullsend-ai/fullsend repository. Results are cached
// so repeated calls for the same ref avoid redundant API calls.
type RefResolver struct {
	client forge.Client
	mu     sync.Mutex
	cache  map[string]string
}

// NewRefResolver creates a resolver that uses the given GitHub client.
// The client must target GitHub's API since fullsend-ai/fullsend is
// always a GitHub repository.
func NewRefResolver(client forge.Client) *RefResolver {
	return &RefResolver{
		client: client,
		cache:  make(map[string]string),
	}
}

// Resolve resolves ref to a commit SHA on fullsend-ai/fullsend. If ref
// is already a SHA it is returned unchanged. Tags are tried first, then
// branches. If resolution fails, ref is returned as-is (graceful
// degradation to string comparison).
func (r *RefResolver) Resolve(ctx context.Context, ref string) string {
	if ref == "" || isSHARef(ref) {
		return ref
	}

	r.mu.Lock()
	if sha, ok := r.cache[ref]; ok {
		r.mu.Unlock()
		return sha
	}
	r.mu.Unlock()

	sha, err := r.client.GetRef(ctx, shimOwner, shimRepo, "tags/"+ref)
	if err != nil {
		sha, err = r.client.GetRef(ctx, shimOwner, shimRepo, "heads/"+ref)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.cache[ref] = ref
		return ref
	}
	r.cache[ref] = sha
	return sha
}

// IsAncestor reports whether potentialAncestor is an ancestor of (or
// identical to) descendant in the fullsend-ai/fullsend repository.
// Both arguments must be commit SHAs. Returns false with a nil error
// when the relationship cannot be determined (e.g., diverged commits).
func (r *RefResolver) IsAncestor(ctx context.Context, potentialAncestor, descendant string) (bool, error) {
	if potentialAncestor == descendant {
		return true, nil
	}
	status, err := r.client.CompareCommits(ctx, shimOwner, shimRepo, potentialAncestor, descendant)
	if err != nil {
		return false, fmt.Errorf("ancestor check %s...%s: %w", potentialAncestor, descendant, err)
	}
	// "ahead" means descendant is ahead of potentialAncestor, so
	// potentialAncestor is indeed an ancestor.
	return status == "ahead", nil
}
