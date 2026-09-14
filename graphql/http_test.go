package graphql_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kylelemons/godebug/pretty"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/schemabuilder"
)

func testHTTPRequest(req *http.Request) *httptest.ResponseRecorder {
	schema := schemabuilder.NewSchema()

	query := schema.Query()
	query.FieldFunc("mirror", func(args struct{ Value int64 }) int64 {
		return args.Value * -1
	})

	builtSchema := schema.MustBuild()

	rr := httptest.NewRecorder()
	handler := graphql.HTTPHandler(builtSchema)

	handler.ServeHTTP(rr, req)
	return rr
}

// postQuery is the happy-path request body used by several tests. The variable
// is declared non-null because mirror's value argument is non-null, and the
// validator rejects a nullable variable in a non-null position.
const postQuery = `{"query": "query TestQuery($value: int64!) { mirror(value: $value) }", "variables": { "value": 1 }}`

func assertBody(t *testing.T, rr *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, but received %d", rr.Code)
	}
	if diff := pretty.Compare(rr.Body.String(), want); diff != "" {
		t.Errorf("expected response to match, but received %s", diff)
	}
}

func TestHTTPMustPost(t *testing.T) {
	req, err := http.NewRequest("GET", "/graphql", nil)
	if err != nil {
		t.Fatal(err)
	}

	// A request that never reached execution carries no "data" key at all.
	assertBody(t, testHTTPRequest(req), `{"errors":[{"message":"request must be a POST"}]}`)
}

func TestHTTPParseQuery(t *testing.T) {
	req, err := http.NewRequest("POST", "/graphql", nil)
	if err != nil {
		t.Fatal(err)
	}

	assertBody(t, testHTTPRequest(req), `{"errors":[{"message":"request must include a query"}]}`)
}

func TestHTTPMustHaveQuery(t *testing.T) {
	req, err := http.NewRequest("POST", "/graphql", strings.NewReader(`{"query":""}`))
	if err != nil {
		t.Fatal(err)
	}

	assertBody(t, testHTTPRequest(req), `{"errors":[{"message":"must have a single query"}]}`)
}

func TestHTTPSuccess(t *testing.T) {
	req, err := http.NewRequest("POST", "/graphql", strings.NewReader(postQuery))
	if err != nil {
		t.Fatal(err)
	}

	assertBody(t, testHTTPRequest(req), `{"data":{"mirror":-1}}`)
}

func TestHTTPContentType(t *testing.T) {
	req, err := http.NewRequest("POST", "/graphql", strings.NewReader(postQuery))
	if err != nil {
		t.Fatal(err)
	}

	rr := testHTTPRequest(req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, but received %d", rr.Code)
	}

	if diff := pretty.Compare(rr.Result().Header.Get("Content-Type"), "application/json"); diff != "" {
		t.Errorf("expected response to match, but received %s", diff)
	}
}

// TestHTTPOperationName checks that a document holding several operations picks
// the one operationName names. Relay always sends operationName.
func TestHTTPOperationName(t *testing.T) {
	body := `{
		"query": "query First($value: int64!) { mirror(value: $value) } query Second($value: int64!) { doubled: mirror(value: $value) }",
		"operationName": "Second",
		"variables": {"value": 3}
	}`

	req, err := http.NewRequest("POST", "/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	assertBody(t, testHTTPRequest(req), `{"data":{"doubled":-3}}`)
}

// TestHTTPAmbiguousOperation checks that a multi-operation document with no
// operationName is rejected rather than silently running the first operation.
func TestHTTPAmbiguousOperation(t *testing.T) {
	body := `{"query": "query First { mirror(value: 1) } query Second { mirror(value: 2) }"}`

	req, err := http.NewRequest("POST", "/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	assertBody(t, testHTTPRequest(req),
		`{"errors":[{"message":"must provide operation name if query contains multiple operations"}]}`)
}

// TestHTTPValidationRejectsUnknownField checks that an illegal query is
// rejected by schema validation before execution starts, with a location.
func TestHTTPValidationRejectsUnknownField(t *testing.T) {
	req, err := http.NewRequest("POST", "/graphql", strings.NewReader(`{"query":"{ nosuchfield }"}`))
	if err != nil {
		t.Fatal(err)
	}

	rr := testHTTPRequest(req)
	body := rr.Body.String()

	if !strings.Contains(body, `Cannot query field \"nosuchfield\" on type \"Query\".`) {
		t.Errorf("expected an unknown field validation error, got %s", body)
	}
	if !strings.Contains(body, `"locations":[{"line":1,"column":3}]`) {
		t.Errorf("expected a source location on the validation error, got %s", body)
	}
	if strings.Contains(body, `"data"`) {
		t.Errorf("a validation failure is a request error and must omit data, got %s", body)
	}
}

// TestHTTPValidationRejectsBadVariableType checks the validator catches a
// nullable variable used where the schema requires a non-null value.
func TestHTTPValidationRejectsBadVariableType(t *testing.T) {
	body := `{"query": "query TestQuery($value: int64) { mirror(value: $value) }", "variables": {"value": 1}}`

	req, err := http.NewRequest("POST", "/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	got := testHTTPRequest(req).Body.String()
	if !strings.Contains(got, `used in position expecting type \"int64!\"`) {
		t.Errorf("expected a variable type validation error, got %s", got)
	}
}
