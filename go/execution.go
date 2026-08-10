package openapiclient

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
)

const (
	defaultMaxDeliveryUnitBytes int64 = 10 << 20
	inputBufferCapacity               = 1
	outputBufferCapacity              = 4
)

// Execution is one prepared operation session. Events preserve order and
// backpressure; Wait reports the terminal outcome after Events closes.
type Execution struct {
	ctx    context.Context
	cancel context.CancelFunc

	inputs        chan any
	inputDone     chan struct{}
	events        chan Event
	done          chan struct{}
	ready         chan struct{}
	responseReady chan struct{}

	mu              sync.Mutex
	inputClosed     bool
	inputRequested  bool
	terminal        bool
	err             error
	cancelErr       error
	diagnostics     Diagnostics
	response        *http.Response
	responseBody    []byte
	responseBodySet bool
	readyOnce       sync.Once
	responseOnce    sync.Once
	doneOnce        sync.Once
	inputOnce       sync.Once
}

func newExecution(parent context.Context) *Execution {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Execution{
		ctx: ctx, cancel: cancel,
		inputs:    make(chan any, inputBufferCapacity),
		inputDone: make(chan struct{}),
		events:    make(chan Event, outputBufferCapacity),
		done:      make(chan struct{}), ready: make(chan struct{}),
		responseReady: make(chan struct{}),
		diagnostics:   Diagnostics{Leading: Metadata{}, Trailing: Metadata{}},
	}
}

// Response returns the native HTTP response observed by this execution. For
// unary exchanges Body is replayable after it has been materialized; for a
// live stream the engine owns Body and the returned snapshot exposes status,
// headers, request, and trailers only.
func (e *Execution) Response(ctx context.Context) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-e.responseReady:
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.cloneResponseLocked(), nil
	case <-e.done:
		select {
		case <-e.responseReady:
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.cloneResponseLocked(), nil
		default:
			return nil, e.terminalError()
		}
	case <-ctx.Done():
		return nil, executionError(CodeCancelled, ctx.Err())
	}
}

func (e *Execution) Send(ctx context.Context, value any) error {
	e.mu.Lock()
	if e.terminal {
		err := e.err
		e.mu.Unlock()
		if err != nil {
			return err
		}
		return &ExecutionError{Code: CodeInvocationClosed, Message: "execution is closed"}
	}
	if e.inputClosed {
		e.mu.Unlock()
		return &ExecutionError{Code: CodeInputClosed, Message: "execution input is closed"}
	}
	e.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case e.inputs <- value:
		return nil
	case <-e.inputDone:
		return &ExecutionError{Code: CodeInputClosed, Message: "execution input is closed"}
	case <-ctx.Done():
		return executionError(CodeCancelled, ctx.Err())
	case <-e.done:
		return e.terminalError()
	}
}

func (e *Execution) FinishInput() error { e.closeInput(); return nil }

func (e *Execution) Cancel() {
	e.mu.Lock()
	if !e.terminal && e.cancelErr == nil {
		e.cancelErr = &ExecutionError{Code: CodeCancelled, Message: "execution cancelled"}
	}
	e.mu.Unlock()
	e.cancel()
}

func (e *Execution) Events() <-chan Event  { return e.events }
func (e *Execution) Done() <-chan struct{} { return e.done }

func (e *Execution) Wait() error {
	<-e.done
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *Execution) Diagnostics() Diagnostics {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Diagnostics{Leading: cloneMetadata(e.diagnostics.Leading), Trailing: cloneMetadata(e.diagnostics.Trailing)}
}

// InputRequested reports whether the artifact execution is waiting for one
// application value. It is stable after PreparedOperation.Start returns.
func (e *Execution) InputRequested() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inputRequested
}

func (e *Execution) requestInput() {
	e.mu.Lock()
	e.inputRequested = true
	e.mu.Unlock()
}

func (e *Execution) signalReady() { e.readyOnce.Do(func() { close(e.ready) }) }

func (e *Execution) closeInput() {
	e.inputOnce.Do(func() {
		e.mu.Lock()
		e.inputClosed = true
		e.mu.Unlock()
		close(e.inputDone)
	})
}

func (e *Execution) nextInput(ctx context.Context) (any, bool, error) {
	for {
		e.mu.Lock()
		closed := e.inputClosed
		e.mu.Unlock()
		select {
		case value := <-e.inputs:
			return value, true, nil
		default:
			if closed {
				return nil, false, nil
			}
		}
		select {
		case value := <-e.inputs:
			return value, true, nil
		case <-e.inputDone:
			// Loop once more so a value accepted before the half-close wins
			// over the close signal. The non-blocking receive above preserves
			// input ordering and prevents an already-accepted value from being
			// discarded by select's randomized ready-case choice.
			continue
		case <-e.done:
			return nil, false, e.terminalError()
		case <-ctx.Done():
			return nil, false, executionError(CodeCancelled, ctx.Err())
		}
	}
}

func (e *Execution) finishAfterRun() {
	e.mu.Lock()
	if e.terminal {
		e.mu.Unlock()
		return
	}
	err := e.cancelErr
	if err == nil && e.ctx.Err() != nil {
		code := CodeCancelled
		if e.ctx.Err() == context.DeadlineExceeded {
			code = CodeTimeout
		}
		err = &ExecutionError{Code: code, Message: e.ctx.Err().Error(), Cause: e.ctx.Err()}
	}
	if err == nil {
		err = &ExecutionError{Code: CodeRuntime, Message: "OpenAPI execution returned without a terminal outcome"}
	}
	e.mu.Unlock()
	e.finish(err)
}

func (e *Execution) emit(ctx context.Context, value any, metadata Metadata) error {
	event := Event{Value: value, Metadata: cloneMetadata(metadata)}
	select {
	case e.events <- event:
		return nil
	case <-e.done:
		return e.terminalError()
	case <-ctx.Done():
		return executionError(CodeCancelled, ctx.Err())
	}
}

func (e *Execution) setHeader(metadata Metadata) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.diagnostics.Leading = cloneMetadata(metadata)
}
func (e *Execution) setTrailer(metadata Metadata) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.diagnostics.Trailing = cloneMetadata(metadata)
}

func (e *Execution) setResponse(response *http.Response) {
	e.mu.Lock()
	e.response = cloneResponseMetadata(response)
	e.mu.Unlock()
	e.responseOnce.Do(func() { close(e.responseReady) })
}

func (e *Execution) setResponseBody(body []byte) {
	e.mu.Lock()
	e.responseBody = append([]byte(nil), body...)
	e.responseBodySet = true
	e.mu.Unlock()
}

func (e *Execution) cloneResponseLocked() *http.Response {
	response := cloneResponseMetadata(e.response)
	if response != nil && e.responseBodySet {
		response.Body = io.NopCloser(bytes.NewReader(append([]byte(nil), e.responseBody...)))
		response.ContentLength = int64(len(e.responseBody))
	}
	return response
}

func cloneResponseMetadata(response *http.Response) *http.Response {
	if response == nil {
		return nil
	}
	clone := new(http.Response)
	*clone = *response
	clone.Header = response.Header.Clone()
	clone.Trailer = response.Trailer.Clone()
	clone.Body = nil
	if response.Request != nil {
		clone.Request = response.Request.Clone(response.Request.Context())
	}
	return clone
}

func (e *Execution) finish(err error) {
	e.doneOnce.Do(func() {
		e.mu.Lock()
		e.terminal = true
		e.inputClosed = true
		e.err = err
		e.mu.Unlock()
		close(e.events)
		close(e.done)
		e.cancel()
	})
}

func (e *Execution) terminalError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	return &ExecutionError{Code: CodeInvocationClosed, Message: "execution is closed"}
}

func cloneMetadata(metadata Metadata) Metadata {
	out := make(Metadata, len(metadata))
	for name, values := range metadata {
		out[name] = append([]string(nil), values...)
	}
	return out
}

func deliveryUnitLimit(value int64) int64 {
	if value > 0 {
		return value
	}
	return defaultMaxDeliveryUnitBytes
}
