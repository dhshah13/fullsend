package mintcore

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// errOIDCNotAuthenticated is returned by verifyOIDCRequest when the
// request cannot be authenticated via OIDC — either the Bearer header
// is missing or token verification failed. Distinct from "valid OIDC
// token that fails authorization", which is a hard policy denial.
var errOIDCNotAuthenticated = errors.New("OIDC: not authenticated")

// verifyOIDCRequest extracts the Bearer token, verifies it via OIDC,
// and runs the full authorization pipeline (AuthorizeToken,
// dual-enrollment, ValidateWorkflowRef). Used by both the /v1/token
// path and the /v1/status auth pipeline.
//
// Returns (claims, isPerRepo, nil) on success. isPerRepo reflects the
// final per-repo mode after dual-enrollment promotion.
//
// Returns errOIDCNotAuthenticated (via errors.Is) when the Bearer
// header is missing or OIDC verification fails — callers that support
// fallback auth can check this. Any other error means the token was
// valid but denied by policy (hard 401, no fallback).
func (h *Handler) verifyOIDCRequest(ctx context.Context, r *http.Request) (*Claims, bool, error) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, false, errOIDCNotAuthenticated
	}
	oidcToken := strings.TrimPrefix(authHeader, "Bearer ")

	claims, err := h.oidcVerifier.Verify(ctx, oidcToken)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", errOIDCNotAuthenticated, err)
	}
	if claims == nil {
		return nil, false, errOIDCNotAuthenticated
	}

	if err := AuthorizeToken(claims, h.allowedOrgs, h.perRepoWIFRepos); err != nil {
		return nil, false, fmt.Errorf("token authorization failed: %w", err)
	}

	isPerRepo := IsPerRepoMode(claims.Repository, h.perRepoWIFRepos)
	isDualEnrolled := false
	if isPerRepo && !IsPublicMintRepos(h.perRepoWIFRepos) &&
		ValidateOrgAllowed(claims.RepositoryOwner, h.allowedOrgs) == nil {
		isDualEnrolled = true
		log.Printf("dual-enrollment: %s matches both per-repo and per-org — accepting workflow refs from either mode", claims.Repository)
		isPerRepo = false
	}
	wfErr := ValidateWorkflowRef(claims.JobWorkflowRef, claims.Repository, isPerRepo, h.workflowHostRepos, h.allowedWorkflowFiles)
	if wfErr != nil && isDualEnrolled {
		wfErr = ValidateWorkflowRef(claims.JobWorkflowRef, claims.Repository, true, h.workflowHostRepos, h.allowedWorkflowFiles)
	}
	if wfErr != nil {
		return nil, false, fmt.Errorf("workflow ref validation failed: %w", wfErr)
	}
	return claims, isPerRepo, nil
}
