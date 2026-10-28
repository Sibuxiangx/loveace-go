package errs

import (
	"errors"
	"strings"
	"testing"
)

func TestCodeStrings(t *testing.T) {
	cases := map[Code]string{
		Network:                "network_error",
		Timeout:                "timeout",
		HTTP:                   "http_error",
		Parse:                  "parse_error",
		InvalidCredentials:     "invalid_credentials",
		MaybeAttacked:          "maybe_attacked",
		Throttled:              "rate_limited",
		NotFound:               "not_found",
		Server:                 "server_error",
		AuthRequired:           "auth_required",
		OutcomeUnknown:         "outcome_unknown",
		InteractionRequired:    "interaction_required",
		TemporarilyUnavailable: "temporarily_unavailable",
		Cancelled:              "cancelled",
		InvalidArgument:        "invalid_argument",
		NonAsciiCredential:     "non_ascii_credential",
		NotImplemented:         "not_implemented",
		Other:                  "error",
	}
	for code, want := range cases {
		if got := code.String(); got != want {
			t.Errorf("Code(%d).String() = %q, want %q", int(code), got, want)
		}
	}
}

func TestRetryable(t *testing.T) {
	for _, c := range []Code{Network, Timeout, HTTP, Throttled, TemporarilyUnavailable} {
		if !c.Retryable() {
			t.Errorf("%s should be retryable", c)
		}
	}
	for _, c := range []Code{InvalidCredentials, MaybeAttacked, InteractionRequired, OutcomeUnknown, Cancelled} {
		if c.Retryable() {
			t.Errorf("%s must not be retryable", c)
		}
	}
}

func TestNonAsciiCredentialMessageNeverContainsValue(t *testing.T) {
	secret := "密码！"
	err := NewNonAsciiCredential("password")
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("value leaked into message: %q", msg)
	}
	if got := err.Code.String(); got != "non_ascii_credential" {
		t.Fatalf("code = %q", got)
	}
	if err.Retryable() {
		t.Fatal("non-ascii credential must not be retryable")
	}
}

func TestErrorClassification(t *testing.T) {
	if !IsInvocation(NewNonAsciiCredential("password")) {
		t.Error("non-ascii credential is an invocation error")
	}
	if !IsAuthOrNetwork(NewInvalidCredentials()) {
		t.Error("invalid credentials is an auth/network error")
	}
	if !IsAuthOrNetwork(NewTimeout(1000)) {
		t.Error("timeout is an auth/network error")
	}
	if IsAuthOrNetwork(NewParse("x")) {
		t.Error("parse error is a business error")
	}
}

func TestErrorsAs(t *testing.T) {
	var e *Error
	err := error(NewNotFound("TwfID"))
	if !errors.As(err, &e) || e.Code != NotFound {
		t.Fatal("errors.As failed for *Error")
	}
	if CodeOf(errors.New("plain")) != Other {
		t.Fatal("plain errors map to Other")
	}
}
