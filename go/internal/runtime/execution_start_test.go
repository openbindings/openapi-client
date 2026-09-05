package openapiclient

import (
	"context"
	"errors"
	"testing"
)

func TestAwaitExecutionStartPreservesReadyExecutionAfterNormalCompletion(t *testing.T) {
	execution := newExecution(context.Background())
	execution.signalReady()
	execution.finish(nil)

	started, err := awaitExecutionStart(context.Background(), execution)
	if err != nil {
		t.Fatalf("awaitExecutionStart: %v", err)
	}
	if started != execution {
		t.Fatalf("awaitExecutionStart returned %p, want %p", started, execution)
	}
}

func TestAwaitExecutionStartReturnsFailureBeforeReady(t *testing.T) {
	execution := newExecution(context.Background())
	want := errors.New("preflight refusal")
	execution.finish(want)

	started, err := awaitExecutionStart(context.Background(), execution)
	if started != nil {
		t.Fatalf("awaitExecutionStart returned execution %p after preflight failure", started)
	}
	if !errors.Is(err, want) {
		t.Fatalf("awaitExecutionStart error = %v, want %v", err, want)
	}
}
