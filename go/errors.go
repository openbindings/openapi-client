package openapiclient

import "fmt"

const (
	CodeContextRequired   = "CONTEXT_REQUIRED"
	CodeCancelled         = "ERR_CANCELLED"
	CodeTimeout           = "ERR_TIMEOUT"
	CodeInputClosed       = "ERR_INPUT_CLOSED"
	CodeInvocationClosed  = "ERR_INVOCATION_CLOSED"
	CodeMissingInput      = "ERR_MISSING_INPUT"
	CodeProtocol          = "ERR_PROTOCOL"
	CodeInvalidRef        = "ERR_INVALID_REF"
	CodeRefNotFound       = "ERR_REF_NOT_FOUND"
	CodeSourceLoadFailed  = "ERR_SOURCE_LOAD_FAILED"
	CodeSourceConfigError = "ERR_SOURCE_CONFIG_ERROR"
	CodeConnectFailed     = "ERR_CONNECT_FAILED"
	CodeExecutionFailed   = "ERR_EXECUTION_FAILED"
	CodeResponseError     = "ERR_RESPONSE_ERROR"
	CodeStreamError       = "ERR_STREAM_ERROR"
	CodeValidationFailed  = "ERR_VALIDATION_FAILED"
	CodeRuntime           = "ERR_RUNTIME"
)

// ExecutionError is an SDK-neutral artifact execution failure.
type ExecutionError struct {
	Code        string
	Message     string
	Details     any
	Evidence    any
	Diagnostics any
	Cause       error
}

func (e *ExecutionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func (e *ExecutionError) Unwrap() error { return e.Cause }

func executionError(code string, err error) *ExecutionError {
	if existing, ok := err.(*ExecutionError); ok {
		return existing
	}
	message := code
	if err != nil {
		message = err.Error()
	}
	return &ExecutionError{Code: code, Message: message, Cause: err}
}

func errorMessage(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case error:
		return typed.Error()
	default:
		return fmt.Sprint(typed)
	}
}
