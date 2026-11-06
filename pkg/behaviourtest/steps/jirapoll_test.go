package steps

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestJiraPoll_CommentSlashCommand(t *testing.T) {
	t.Parallel()

	w := &world.World{Logf: t.Logf}

	require.NoError(t, givenMockJiraServer(w))
	t.Cleanup(func() {
		CleanupScenario(w)
	})

	require.NoError(t, givenJiraIssue(w, "PROJ-101", "bug"))
	require.NoError(t, whenJiraComment(w, "PROJ-101", "/fs-triage check acceptance criteria"))
	require.NoError(t, whenJiraPollerRuns(context.Background(), w))
	require.NoError(t, thenDispatchContains(w, "triage", "PROJ-101"))
}

func TestJiraPoll_LabelChange(t *testing.T) {
	t.Parallel()

	w := &world.World{Logf: t.Logf}

	require.NoError(t, givenMockJiraServer(w))
	t.Cleanup(func() {
		CleanupScenario(w)
	})

	require.NoError(t, givenJiraIssue(w, "PROJ-202", "backlog"))
	require.NoError(t, whenJiraLabelAdded(w, "PROJ-202", "ready-to-code"))
	require.NoError(t, whenJiraPollerRuns(context.Background(), w))
	require.NoError(t, thenDispatchContains(w, "code", "PROJ-202"))
	require.NoError(t, thenDispatchNotContains(w, "PROJ-999"))
}

func TestJiraPoll_NoDispatchForUnknownIssue(t *testing.T) {
	t.Parallel()

	w := &world.World{Logf: t.Logf}

	require.NoError(t, givenMockJiraServer(w))
	t.Cleanup(func() {
		CleanupScenario(w)
	})

	require.NoError(t, givenJiraIssue(w, "PROJ-300", "bug"))
	// No comment or label change — only "opened" event, which doesn't route.
	require.NoError(t, whenJiraPollerRuns(context.Background(), w))
	require.NoError(t, thenDispatchNotContains(w, "PROJ-300"))
}

func TestJiraPoll_MissingMockServer(t *testing.T) {
	t.Parallel()

	w := &world.World{Logf: t.Logf}

	err := givenJiraIssue(w, "PROJ-1", "bug")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}
