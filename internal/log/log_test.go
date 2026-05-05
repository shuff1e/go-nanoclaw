package log

import (
	"testing"
)

func TestSetVerbosityAndVerbosity(t *testing.T) {
	SetVerbosity(0)
	if Verbosity() != 0 {
		t.Fatalf("expected 0, got %d", Verbosity())
	}

	SetVerbosity(1)
	if Verbosity() != 1 {
		t.Fatalf("expected 1, got %d", Verbosity())
	}

	SetVerbosity(2)
	if Verbosity() != 2 {
		t.Fatalf("expected 2, got %d", Verbosity())
	}
}

func TestResetSteps(t *testing.T) {
	ResetSteps()
	n1 := nextStep()
	n2 := nextStep()
	if n1 != 1 || n2 != 2 {
		t.Fatalf("expected step sequence 1,2, got %d,%d", n1, n2)
	}

	ResetSteps()
	n3 := nextStep()
	if n3 != 1 {
		t.Fatalf("expected step 1 after reset, got %d", n3)
	}
}

func TestFormatKVs(t *testing.T) {
	tests := []struct {
		name string
		kvs  []any
		want string
	}{
		{"empty", nil, ""},
		{"single", []any{"key", "val"}, "key=val"},
		{"multiple", []any{"a", 1, "b", "two"}, "a=1  b=two"},
		{"odd", []any{"only_key"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatKVs(tt.kvs...)
			if got != tt.want {
				t.Errorf("formatKVs(%v) = %q, want %q", tt.kvs, got, tt.want)
			}
		})
	}
}

func TestFormatKVsTruncates(t *testing.T) {
	longVal := string(make([]byte, 100))
	got := formatKVs("key", longVal)
	if len(got) > 85 { // "key=" (4) + 77 + "..." (3) + margin
		t.Errorf("expected truncation, got len=%d", len(got))
	}
}

func TestPrettyJSON(t *testing.T) {
	valid := `{"key":"value"}`
	pretty := prettyJSON(valid)
	if pretty == valid {
		t.Error("expected pretty JSON to differ from compact")
	}
	if pretty != "{\n  \"key\": \"value\"\n}" {
		t.Errorf("unexpected pretty JSON: %s", pretty)
	}

	invalid := "not json"
	if prettyJSON(invalid) != invalid {
		t.Error("expected non-JSON to pass through unchanged")
	}
}

func TestIsSensitiveHeader(t *testing.T) {
	sensitive := []string{"Authorization", "X-API-Key", "api_token", "x-auth-token"}
	for _, h := range sensitive {
		if !isSensitiveHeader(h) {
			t.Errorf("expected %q to be sensitive", h)
		}
	}

	safe := []string{"Content-Type", "Accept", "User-Agent"}
	for _, h := range safe {
		if isSensitiveHeader(h) {
			t.Errorf("expected %q to NOT be sensitive", h)
		}
	}
}

func TestVerbosityGuardsDoNotPanic(t *testing.T) {
	// These functions should be no-ops at verbosity 0 without panicking
	SetVerbosity(0)

	Section("test section")
	Step("test step")
	SubStep("test sub")
	Result("test result")
	Warn("test warn")
	Banner("test", "banner")
	Narrative("label", "explanation")
	Tree(0, false, "line")
	Completion("done")
	TraceHTTPRequest("GET", "http://example.com", nil, "{}")
	TraceHTTPResponse(200, "{}")
	Flow("method", "step")
	Trace("msg")
}

func TestInfoAlwaysWrites(t *testing.T) {
	// Info should write regardless of verbosity level
	SetVerbosity(0)
	// Just verify it doesn't panic
	Info("test message", "key", "value")
}
