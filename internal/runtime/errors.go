package runtime

import (
	"errors"
	"fmt"
	"net/http"
)

type ErrCode string

const (
	CodeInvalidInput       ErrCode = "invalid_input"
	CodeCancelled          ErrCode = "cancelled"
	CodeTimeout            ErrCode = "timeout"
	CodeToolFailed         ErrCode = "tool_failed"
	CodeToolDenied         ErrCode = "tool_denied"
	CodeApprovalRequired   ErrCode = "approval_required"
	CodeBrainFailed        ErrCode = "brain_failed"
	CodeVerificationFailed ErrCode = "verification_failed"
	CodeInternal           ErrCode = "internal"
)

type AgentError struct {
	Code    ErrCode
	Message string
	Err     error
}

func (e *AgentError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		if e.Message != "" {
			return e.Message + ": " + e.Err.Error()
		}
		return e.Err.Error()
	}
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}

func (e *AgentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func Errorf(code ErrCode, format string, args ...any) *AgentError {
	return &AgentError{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
	}
}

func Wrap(code ErrCode, err error, format string, args ...any) *AgentError {
	msg := fmt.Sprintf(format, args...)
	return &AgentError{
		Code:    code,
		Message: msg,
		Err:     err,
	}
}

func CodeOf(err error) ErrCode {
	var agentErr *AgentError
	if errors.As(err, &agentErr) && agentErr.Code != "" {
		return agentErr.Code
	}
	return CodeInternal
}

func HTTPStatus(err error) int {
	switch CodeOf(err) {
	case CodeInvalidInput:
		return http.StatusBadRequest
	case CodeCancelled, CodeVerificationFailed:
		return http.StatusConflict
	case CodeApprovalRequired:
		return http.StatusConflict
	case CodeToolDenied:
		return http.StatusForbidden
	case CodeTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

func Retryable(err error) bool {
	switch CodeOf(err) {
	case CodeTimeout, CodeToolFailed, CodeBrainFailed:
		return true
	default:
		return false
	}
}
