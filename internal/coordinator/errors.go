package coordinator

import "fmt"

type operationError struct {
	Code           string
	Message        string
	Retryable      bool
	OutcomeUnknown bool
}

func (e *operationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

func opError(code, message string, retryable bool) error {
	return &operationError{Code: code, Message: message, Retryable: retryable}
}

func wrapOpError(code, message string, retryable, unknown bool, err error) error {
	if err != nil && message == "" {
		message = err.Error()
	}
	if message == "" {
		message = fmt.Sprintf("%s failed", code)
	}
	return &operationError{Code: code, Message: message, Retryable: retryable, OutcomeUnknown: unknown}
}
