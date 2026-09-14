package graphql

import (
	"encoding/json"
	"strings"
)

// This file defines the wire shape of a GraphQL response, following
// https://spec.graphql.org/October2021/#sec-Response.

// ErrorLocation is a position in the query document an error refers to.
type ErrorLocation struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// ResponseError is a single entry in a response's "errors" list.
//
// Message is required. Path is the response path to the field that raised the
// error, made up of field names (string) and list indices (int); Relay's
// partial-data handling reads it to decide which part of the store an error
// applies to.
type ResponseError struct {
	Message    string                 `json:"message"`
	Locations  []ErrorLocation        `json:"locations,omitempty"`
	Path       []interface{}          `json:"path,omitempty"`
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

func (e *ResponseError) Error() string {
	if len(e.Path) == 0 {
		return e.Message
	}
	return formatPath(e.Path) + ": " + e.Message
}

// Response is the top level of a GraphQL response.
//
// HasData distinguishes the two cases the specification keeps apart: a request
// error, where execution never began and "data" must be absent entirely, and a
// field error, where execution ran and "data" is present even if it is null.
type Response struct {
	Data       interface{}
	HasData    bool
	Errors     []*ResponseError
	Extensions map[string]interface{}
}

// NewResponse builds the response for a completed execution.
func NewResponse(data interface{}, err error) *Response {
	return &Response{Data: data, HasData: true, Errors: AsResponseErrors(err)}
}

// NewRequestErrorResponse builds the response for a request that failed before
// execution began.
func NewRequestErrorResponse(err error) *Response {
	return &Response{Errors: AsResponseErrors(err)}
}

// MarshalJSON writes the response with "data" present only when execution ran.
func (r *Response) MarshalJSON() ([]byte, error) {
	out := map[string]interface{}{}
	if r.HasData {
		out["data"] = r.Data
	}
	if len(r.Errors) > 0 {
		out["errors"] = r.Errors
	}
	if len(r.Extensions) > 0 {
		out["extensions"] = r.Extensions
	}
	return json.Marshal(out)
}

// MultiError carries more than one ResponseError as a single Go error. Query
// validation produces one, because a document can be wrong in several ways at
// once.
type MultiError struct {
	Errors []*ResponseError
}

func (e *MultiError) Error() string {
	messages := make([]string, 0, len(e.Errors))
	for _, err := range e.Errors {
		messages = append(messages, err.Error())
	}
	return strings.Join(messages, "; ")
}

// SanitizedError implements SanitizedError: every ResponseError in a MultiError
// is already client-facing.
func (e *MultiError) SanitizedError() string {
	return e.Error()
}

// AsResponseErrors converts an error raised while serving a request into the
// list of errors to put in the response.
//
// Errors that are not explicitly client-safe are replaced by a generic message,
// exactly as SanitizeError does, so that internal detail cannot leak.
func AsResponseErrors(err error) []*ResponseError {
	if err == nil {
		return nil
	}

	if multi, ok := err.(*MultiError); ok {
		return multi.Errors
	}

	if pe, ok := err.(*pathError); ok {
		return []*ResponseError{{
			Message: SanitizeError(pe.inner),
			Path:    pe.responsePath(),
		}}
	}

	return []*ResponseError{{Message: SanitizeError(err)}}
}

// formatPath renders a response path the way error messages have always
// rendered it, as dot-separated segments with list indices in brackets.
func formatPath(path []interface{}) string {
	var b strings.Builder
	for i, segment := range path {
		switch segment := segment.(type) {
		case int:
			b.WriteString("[")
			b.WriteString(itoa(segment))
			b.WriteString("]")
		default:
			if i > 0 {
				b.WriteString(".")
			}
			b.WriteString(toString(segment))
		}
	}
	return b.String()
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
