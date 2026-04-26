package runtime

import (
	"errors"
	"net/http"
	"testing"
)

func TestHTTPStatusMapsTimeout(t *testing.T) {
	err := Wrap(CodeTimeout, errors.New("deadline"), "timed out")
	if got := HTTPStatus(err); got != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", got)
	}
}

func TestHTTPStatusMapsCancelled(t *testing.T) {
	err := Wrap(CodeCancelled, errors.New("stopped"), "task cancelled")
	if got := HTTPStatus(err); got != http.StatusConflict {
		t.Fatalf("expected 409, got %d", got)
	}
}

func TestHTTPStatusMapsVerificationFailed(t *testing.T) {
	err := Errorf(CodeVerificationFailed, "quality gate failed")
	if got := HTTPStatus(err); got != http.StatusConflict {
		t.Fatalf("expected 409, got %d", got)
	}
}

func TestHTTPStatusMapsToolDenied(t *testing.T) {
	err := Errorf(CodeToolDenied, "run_command denied")
	if got := HTTPStatus(err); got != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", got)
	}
}

func TestCodeOfDefaultsToInternal(t *testing.T) {
	if got := CodeOf(errors.New("plain")); got != CodeInternal {
		t.Fatalf("expected internal code, got %s", got)
	}
}

func TestAgentErrorErrorIncludesWrappedCause(t *testing.T) {
	err := Wrap(CodeBrainFailed, errors.New("dial tcp: lookup zenmux.ai: no such host"), "brain think")
	if got := err.Error(); got != "brain think: dial tcp: lookup zenmux.ai: no such host" {
		t.Fatalf("unexpected error string: %q", got)
	}
}

func TestAgentErrorErrorFallsBackToMessage(t *testing.T) {
	err := Errorf(CodeInvalidInput, "missing input text")
	if got := err.Error(); got != "missing input text" {
		t.Fatalf("unexpected error string: %q", got)
	}
}

func TestRetryableErrorPolicy(t *testing.T) {
	retryable := []ErrCode{CodeTimeout, CodeToolFailed, CodeBrainFailed}
	for _, code := range retryable {
		if !Retryable(Errorf(code, "retry me")) {
			t.Fatalf("expected %s to be retryable", code)
		}
	}

	notRetryable := []ErrCode{CodeInvalidInput, CodeCancelled, CodeVerificationFailed, CodeToolDenied, CodeInternal}
	for _, code := range notRetryable {
		if Retryable(Errorf(code, "do not retry")) {
			t.Fatalf("expected %s to be non-retryable", code)
		}
	}
}

func TestParseExecutionModeIncludesVerifiedPlan(t *testing.T) {
	mode, ok := ParseExecutionMode("plan_execute_verify")
	if !ok || mode != ModePlanExecuteVerify {
		t.Fatalf("expected verified plan mode, got %q ok=%v", mode, ok)
	}
	if !IsPlannedMode(mode) {
		t.Fatalf("expected %q to be planned mode", mode)
	}
}
