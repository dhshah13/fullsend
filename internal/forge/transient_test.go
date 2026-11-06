package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeTransientErr implements the transientReporter interface used by
// forge.IsTransient to let forge-specific API errors self-report
// transient-ness.
type fakeTransientErr struct {
	transient bool
}

func (e *fakeTransientErr) Error() string { return "fake error" }
func (e *fakeTransientErr) IsTransient() bool {
	return e.transient
}

// fakeTimeoutErr implements the Timeout() interface to simulate HTTP
// client timeout errors.
type fakeTimeoutErr struct {
	timeout bool
}

func (e *fakeTimeoutErr) Error() string   { return "timeout error" }
func (e *fakeTimeoutErr) Timeout() bool   { return e.timeout }
func (e *fakeTimeoutErr) Temporary() bool { return e.timeout }

func TestIsTransient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "ErrNonFastForward",
			err:  ErrNonFastForward,
			want: true,
		},
		{
			name: "wrapped ErrNonFastForward",
			err:  fmt.Errorf("commit failed: %w", ErrNonFastForward),
			want: true,
		},
		{
			name: "transient reporter true",
			err:  &fakeTransientErr{transient: true},
			want: true,
		},
		{
			name: "transient reporter false",
			err:  &fakeTransientErr{transient: false},
			want: false,
		},
		{
			name: "wrapped transient reporter",
			err:  fmt.Errorf("api call: %w", &fakeTransientErr{transient: true}),
			want: true,
		},
		{
			name: "context.DeadlineExceeded is not transient",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "wrapped context.DeadlineExceeded is not transient",
			err:  fmt.Errorf("timed out: %w", context.DeadlineExceeded),
			want: false,
		},
		{
			name: "context.Canceled is not transient",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "wrapped context.Canceled is not transient",
			err:  fmt.Errorf("canceled: %w", context.Canceled),
			want: false,
		},
		{
			name: "timeout error",
			err:  &fakeTimeoutErr{timeout: true},
			want: true,
		},
		{
			name: "non-timeout error with Timeout method",
			err:  &fakeTimeoutErr{timeout: false},
			want: false,
		},
		{
			name: "io.EOF",
			err:  io.EOF,
			want: true,
		},
		{
			name: "wrapped io.EOF",
			err:  fmt.Errorf("read body: %w", io.EOF),
			want: true,
		},
		{
			name: "io.ErrUnexpectedEOF",
			err:  io.ErrUnexpectedEOF,
			want: true,
		},
		{
			name: "ErrNotFound is not transient",
			err:  ErrNotFound,
			want: false,
		},
		{
			name: "ErrForbidden is not transient",
			err:  ErrForbidden,
			want: false,
		},
		{
			name: "ErrBranchProtected is not transient",
			err:  ErrBranchProtected,
			want: false,
		},
		{
			name: "generic error is not transient",
			err:  errors.New("something broke"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsTransient(tt.err))
		})
	}
}
