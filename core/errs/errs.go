// Package errs defines the typed error surface for the LoveACE core.
//
// Every fallible core operation returns *Error carrying a stable machine
// readable Code. The categories mirror the Rust CoreError: network
// availability, authentication outcomes, rate limiting, parser drift,
// interaction requirements, cancellation and the deliberately distinct
// outcome_unknown state for writes whose server outcome could not be
// confirmed.
package errs

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error code exposed through CLI/FFI
// envelopes. The zero value is Other.
type Code int

// Error codes. The String() spellings must match the Rust ErrorCode::as_str
// list exactly; they are the wire format for JSON envelopes and telemetry.
const (
	Other Code = iota
	Network
	Timeout
	HTTP
	Parse
	InvalidCredentials
	MaybeAttacked
	Throttled
	NotFound
	Server
	AuthRequired
	OutcomeUnknown
	InteractionRequired
	TemporarilyUnavailable
	Cancelled
	InvalidArgument
	NonAsciiCredential
	NotImplemented
	UnsupportedManifestVersion
	PasswordChangeRequired
)

var codeStrings = map[Code]string{
	Network:                    "network_error",
	Timeout:                    "timeout",
	HTTP:                       "http_error",
	Parse:                      "parse_error",
	InvalidCredentials:         "invalid_credentials",
	MaybeAttacked:              "maybe_attacked",
	Throttled:                  "rate_limited",
	NotFound:                   "not_found",
	Server:                     "server_error",
	AuthRequired:               "auth_required",
	OutcomeUnknown:             "outcome_unknown",
	InteractionRequired:        "interaction_required",
	TemporarilyUnavailable:     "temporarily_unavailable",
	Cancelled:                  "cancelled",
	InvalidArgument:            "invalid_argument",
	NonAsciiCredential:         "non_ascii_credential",
	PasswordChangeRequired:     "password_change_required",
	NotImplemented:             "not_implemented",
	Other:                      "error",
	UnsupportedManifestVersion: "parse_error",
}

// String returns the snake-case wire spelling used in JSON envelopes.
func (c Code) String() string {
	if s, ok := codeStrings[c]; ok {
		return s
	}
	return "error"
}

// Retryable reports whether the same logical operation may be retried
// without user input.
func (c Code) Retryable() bool {
	switch c {
	case Network, Timeout, HTTP, Throttled, TemporarilyUnavailable:
		return true
	default:
		return false
	}
}

// Error is the core error type.
type Error struct {
	Code Code
	Msg  string
	// Status is the HTTP status code for HTTP errors.
	Status int
	// ServerCode is the business code returned by a school service.
	ServerCode int64
	// RetryAfterMS is the suggested backoff for throttled errors.
	RetryAfterMS int64
	// Field is the offending credential field name for non-ASCII credential
	// errors. The field value itself must never be included.
	Field string
	// Value is the parsed schema version for unsupported manifest errors.
	Value int64
}

// Error implements the error interface. Messages never contain credentials.
func (e *Error) Error() string {
	switch e.Code {
	case Network:
		return "network error: " + e.Msg
	case Timeout:
		return fmt.Sprintf("request timed out after %s ms", e.Msg)
	case HTTP:
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Msg)
	case InvalidCredentials:
		return "invalid credentials"
	case MaybeAttacked:
		return "anti-bot throttling or CAPTCHA required"
	case Throttled:
		return fmt.Sprintf("rate limited (retry after %d ms)", e.RetryAfterMS)
	case NotFound:
		return "not found: " + e.Msg
	case Server:
		return fmt.Sprintf("server error code %d: %s", e.ServerCode, e.Msg)
	case AuthRequired:
		return "authentication required"
	case OutcomeUnknown:
		return "outcome unknown: " + e.Msg
	case InteractionRequired:
		return "interaction required: " + e.Msg
	case TemporarilyUnavailable:
		return "temporarily unavailable: " + e.Msg
	case Cancelled:
		return "operation cancelled"
	case InvalidArgument:
		return "invalid argument: " + e.Msg
	case NonAsciiCredential:
		return fmt.Sprintf("credential field '%s' contains non-ASCII characters (full-width/Chinese input?); use half-width input", e.Field)
	case NotImplemented:
		return "not implemented: " + e.Msg
	case UnsupportedManifestVersion:
		return fmt.Sprintf("unsupported manifest schema version: %d", e.Value)
	default:
		return "error: " + e.Msg
	}
}

// Retryable reports whether a blind retry is safe. Writes are excluded upstream.
func (e *Error) Retryable() bool { return e.Code.Retryable() }

// Constructors mirroring the Rust CoreError variants.

func NewNetwork(msg string) *Error   { return &Error{Code: Network, Msg: msg} }
func NewTimeout(ms int64) *Error     { return &Error{Code: Timeout, Msg: fmt.Sprint(ms)} }
func NewParse(msg string) *Error     { return &Error{Code: Parse, Msg: msg} }
func NewNotFound(what string) *Error { return &Error{Code: NotFound, Msg: what} }
func NewCancelled() *Error           { return &Error{Code: Cancelled} }

func NewInvalidArgument(msg string) *Error { return &Error{Code: InvalidArgument, Msg: msg} }

// NewPasswordChangeRequired: the school accepted the credentials (the CAS
// even minted a TGT) but intercepts every login with its mandatory
// password-change flow (/mng/acp/completeInfo/password). Nothing the
// client can submit resolves it — the user must change the password
// through the school's own portal, then retry.
func NewPasswordChangeRequired() *Error {
	return &Error{Code: PasswordChangeRequired, Msg: "school demands a password change"}
}
func NewNotImplemented(msg string) *Error { return &Error{Code: NotImplemented, Msg: msg} }
func NewOutcomeUnknown(msg string) *Error { return &Error{Code: OutcomeUnknown, Msg: msg} }

func NewInteractionRequired(msg string) *Error {
	return &Error{Code: InteractionRequired, Msg: msg}
}

func NewTemporarilyUnavailable(msg string) *Error {
	return &Error{Code: TemporarilyUnavailable, Msg: msg}
}

func NewInvalidCredentials() *Error { return &Error{Code: InvalidCredentials} }

func NewAuthRequiredSimple() *Error { return &Error{Code: AuthRequired} }

func NewMaybeAttacked() *Error { return &Error{Code: MaybeAttacked} }

func NewHTTP(status int, message string) *Error {
	return &Error{Code: HTTP, Status: status, Msg: message}
}

func NewServer(code int64, message string) *Error {
	return &Error{Code: Server, ServerCode: code, Msg: message}
}

func NewThrottled(retryAfterMS int64) *Error {
	return &Error{Code: Throttled, RetryAfterMS: retryAfterMS}
}

func NewNonAsciiCredential(field string) *Error {
	return &Error{Code: NonAsciiCredential, Field: field}
}

func NewUnsupportedManifestVersion(v int64) *Error {
	return &Error{Code: UnsupportedManifestVersion, Value: v}
}

// IsAuthOrNetwork reports whether the error belongs to the auth/network class
// used for CLI exit-code mapping.
func IsAuthOrNetwork(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case AuthRequired, InvalidCredentials, MaybeAttacked, Throttled,
		InteractionRequired, Network, Timeout, HTTP, TemporarilyUnavailable:
		return true
	default:
		return false
	}
}

// IsInvocation reports whether the error belongs to the invocation class
// (argument errors, non-ASCII credentials, unimplemented operations).
func IsInvocation(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case InvalidArgument, NonAsciiCredential, NotImplemented:
		return true
	default:
		return false
	}
}

// CodeOf extracts the stable Code from an error (Other for non-core errors).
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Other
}
