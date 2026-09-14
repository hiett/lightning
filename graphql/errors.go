package graphql

import (
	"fmt"

	"github.com/gorilla/websocket"
)

type SanitizedError interface {
	error
	SanitizedError() string
}

type SafeError struct {
	inner   error
	message string
}

type ClientError SafeError

func (e ClientError) Error() string {
	return e.message
}

func (e ClientError) SanitizedError() string {
	return e.message
}

func (e SafeError) Error() string {
	return e.message
}

func (e SafeError) SanitizedError() string {
	return e.message
}

func (e SafeError) Reason() string {
	return e.message
}

// Unwrap returns the wrapped error, implementing go's 1.13 error wrapping proposal.
func (e SafeError) Unwrap() error {
	return e.inner
}

func NewClientError(format string, a ...interface{}) error {
	return ClientError{message: fmt.Sprintf(format, a...)}
}

func NewSafeError(format string, a ...interface{}) error {
	return SafeError{message: fmt.Sprintf(format, a...)}
}

// WrapAsSafeError wraps an error into a "SafeError", and takes in a message.
// This message can be used like fmt.Sprintf to take in formatting and arguments.
func WrapAsSafeError(err error, format string, a ...interface{}) error {
	return SafeError{inner: err, message: fmt.Sprintf(format, a...)}
}

// IsSanitized reports whether an error is safe to show a client.
//
// It looks through the path decoration the executor adds as an error unwinds,
// which a bare `err.(SanitizedError)` type assertion does not: a client-safe
// error that came from a field several levels down is still client-safe.
func IsSanitized(err error) bool {
	if pe, ok := err.(*pathError); ok {
		return IsSanitized(pe.inner)
	}
	_, ok := err.(SanitizedError)
	return ok
}

// SanitizeError returns a sanitized error message for an error.
//
// It looks through a path decoration, so that an error which was safe to show a
// client stays safe to show them after the executor has recorded where it came
// from.
func SanitizeError(err error) string {
	if pe, ok := err.(*pathError); ok {
		return SanitizeError(pe.inner)
	}
	if sanitized, ok := err.(SanitizedError); ok {
		return sanitized.SanitizedError()
	}
	return "Internal server error"
}

func isCloseError(err error) bool {
	_, ok := err.(*websocket.CloseError)
	return ok || err == websocket.ErrCloseSent
}
