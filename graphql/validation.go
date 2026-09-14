package graphql

import (
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
	_ "github.com/vektah/gqlparser/v2/validator/rules"
)

// A Validator checks query documents against a schema before they are executed.
//
// Without one, an illegal query is only noticed field by field during
// execution, which produces worse errors and does more work before failing.
type Validator struct {
	schema    *Schema
	astSchema *ast.Schema
}

// NewValidator builds a Validator for a schema.
//
// It fails if the schema cannot be rendered as legal SDL, which makes it a
// useful check on a freshly built schema even if queries are never validated.
func NewValidator(schema *Schema) (*Validator, error) {
	astSchema, err := ASTSchema(schema)
	if err != nil {
		return nil, err
	}
	return &Validator{schema: schema, astSchema: astSchema}, nil
}

// MustNewValidator is NewValidator, panicking on failure.
func MustNewValidator(schema *Schema) *Validator {
	v, err := NewValidator(schema)
	if err != nil {
		panic(err)
	}
	return v
}

// ASTSchema returns the gqlparser schema queries are validated against.
func (v *Validator) ASTSchema() *ast.Schema {
	return v.astSchema
}

// Schema returns the runtime schema the Validator was built from.
func (v *Validator) Schema() *Schema {
	return v.schema
}

// Parse parses a query, checks it against the schema, and returns the operation
// named operationName.
//
// An empty operationName selects the document's only operation.
func (v *Validator) Parse(source string, vars map[string]interface{}, operationName string) (*Query, error) {
	document, err := parser.ParseQuery(&ast.Source{Input: source})
	if err != nil {
		return nil, gqlErrorsToMultiError(asGQLErrorList(err))
	}

	if errs := validator.Validate(v.astSchema, document); len(errs) > 0 {
		return nil, gqlErrorsToMultiError(errs)
	}

	return parseDocument(document, vars, operationName)
}

func asGQLErrorList(err error) gqlerror.List {
	switch err := err.(type) {
	case gqlerror.List:
		return err
	case *gqlerror.Error:
		return gqlerror.List{err}
	default:
		return gqlerror.List{gqlerror.Wrap(err)}
	}
}

// gqlErrorsToMultiError converts gqlparser's errors into response errors.
func gqlErrorsToMultiError(errs gqlerror.List) error {
	converted := make([]*ResponseError, 0, len(errs))
	for _, err := range errs {
		converted = append(converted, gqlErrorToResponseError(err))
	}
	return &MultiError{Errors: converted}
}

func gqlErrorToResponseError(err *gqlerror.Error) *ResponseError {
	out := &ResponseError{Message: err.Message}

	for _, location := range err.Locations {
		out.Locations = append(out.Locations, ErrorLocation{
			Line:   location.Line,
			Column: location.Column,
		})
	}

	for _, segment := range err.Path {
		switch segment := segment.(type) {
		case ast.PathName:
			out.Path = append(out.Path, string(segment))
		case ast.PathIndex:
			out.Path = append(out.Path, int(segment))
		}
	}

	if len(err.Extensions) > 0 {
		out.Extensions = err.Extensions
	}

	return out
}
