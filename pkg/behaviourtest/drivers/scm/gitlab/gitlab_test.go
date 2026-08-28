package gitlab

import (
	"context"
	"errors"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestDeleteBranch(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	err := d.DeleteBranch(context.Background(), "owner", "repo", "feature-branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fc.DeletedRefs) != 1 {
		t.Fatalf("expected 1 deleted ref, got %d", len(fc.DeletedRefs))
	}
	// DeleteBranch should prepend "heads/" to the branch name.
	if fc.DeletedRefs[0] != "owner/repo/heads/feature-branch" {
		t.Errorf("expected ref %q, got %q", "owner/repo/heads/feature-branch", fc.DeletedRefs[0])
	}
}

func TestDeleteBranch_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["DeleteRef"] = errors.New("ref delete failed")
	d := New(fc)

	err := d.DeleteBranch(context.Background(), "owner", "repo", "branch")
	if err == nil || err.Error() != "ref delete failed" {
		t.Fatalf("expected ref delete failed error, got %v", err)
	}
}

func TestCreateRepo_IdempotentAlreadyExists(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	// First create succeeds.
	err := d.CreateRepo(context.Background(), "org", "my-repo", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second create of the same repo should succeed (idempotent).
	err = d.CreateRepo(context.Background(), "org", "my-repo", "desc")
	if err != nil {
		t.Fatalf("idempotent CreateRepo should not error: %v", err)
	}
}

func TestCreateRepo_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateRepo"] = errors.New("create failed")
	d := New(fc)

	err := d.CreateRepo(context.Background(), "org", "my-repo", "desc")
	if err == nil || err.Error() != "create failed" {
		t.Fatalf("expected create failed error, got %v", err)
	}
}

func TestCreateForkChangeProposal_DiscardsBaseOwnerRepo(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	// GitLab fork MRs are created against the fork project with a plain
	// branch name. The baseOwner and baseRepo parameters are ignored
	// (blank identifiers in the implementation) because GitLab
	// automatically targets the upstream project.
	cp, err := d.CreateForkChangeProposal(context.Background(),
		"upstream", "upstream-repo", // baseOwner, baseRepo — ignored
		"MR title", "MR body",
		"fork-user", "fork-repo", // forkOwner, forkRepo — used
		"feature-branch", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cp == nil {
		t.Fatal("expected non-nil ChangeProposal")
	}
	if cp.Title != "MR title" {
		t.Errorf("expected title %q, got %q", "MR title", cp.Title)
	}
	if cp.Base != "main" {
		t.Errorf("expected base %q, got %q", "main", cp.Base)
	}

	// Verify the proposal was created against the fork project (not the
	// upstream) with a plain branch name (not "owner:branch").
	if len(fc.CreatedProposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(fc.CreatedProposals))
	}
	if fc.CreatedProposals[0].Head != "feature-branch" {
		t.Errorf("expected plain head %q, got %q", "feature-branch", fc.CreatedProposals[0].Head)
	}
}

func TestCreateForkChangeProposal_SameOwner(t *testing.T) {
	fc := forge.NewFakeClient()
	d := New(fc)

	// Same-owner fork: GitLab still uses the fork project's MR endpoint
	// with a plain branch name (no special cross-repo logic needed).
	cp, err := d.CreateForkChangeProposal(context.Background(),
		"org", "repo",
		"MR title", "MR body",
		"org", "repo-fork",
		"feature-branch", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cp == nil {
		t.Fatal("expected non-nil ChangeProposal")
	}

	if len(fc.CreatedProposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(fc.CreatedProposals))
	}
	// Even when owners match, GitLab uses the plain branch name.
	if fc.CreatedProposals[0].Head != "feature-branch" {
		t.Errorf("expected plain head %q, got %q", "feature-branch", fc.CreatedProposals[0].Head)
	}
}

func TestCreateForkChangeProposal_Error(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateChangeProposal"] = errors.New("mr failed")
	d := New(fc)

	_, err := d.CreateForkChangeProposal(context.Background(),
		"upstream", "repo", "title", "body",
		"fork-user", "fork-repo", "branch", "main")
	if err == nil || err.Error() != "mr failed" {
		t.Fatalf("expected mr failed error, got %v", err)
	}
}

func TestParseRepo_Success(t *testing.T) {
	owner, repo, err := ParseRepo("acme/widget")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "acme" || repo != "widget" {
		t.Errorf("expected acme/widget, got %s/%s", owner, repo)
	}
}

func TestParseRepo_Invalid(t *testing.T) {
	_, _, err := ParseRepo("invalid")
	if err == nil {
		t.Fatal("expected error for invalid repo name")
	}
}
