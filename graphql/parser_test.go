package graphql_test

import (
	"reflect"
	"testing"

	. "github.com/hiett/lightning/graphql"
)

func TestParseSupported(t *testing.T) {
	query, err := Parse(`
{
	foo {
		alias: bar
		alias: bar
		baz(arg: 3) {
			bah(x: 1, y: "123", z: true)
			hum(foo: {x: $var}, bug: [1, 2, [4, 5]])
		}
		... on Foo {
			asd
			... Bar
		}
	}
	xyz
}

fragment Bar on Foo {
	zxc
}`, map[string]interface{}{
		"var": "var value!!",
	})
	if err != nil {
		t.Error("unexpected error", err)
	}

	expected := &Query{
		Name: "",
		Kind: "query",
		SelectionSet: &SelectionSet{
			Selections: []*Selection{
				{
					Name:         "foo",
					Alias:        "foo",
					UnparsedArgs: map[string]interface{}{},
					SelectionSet: &SelectionSet{
						Selections: []*Selection{
							{
								Name:         "bar",
								Alias:        "alias",
								UnparsedArgs: map[string]interface{}{},
							},
							{
								Name:         "bar",
								Alias:        "alias",
								UnparsedArgs: map[string]interface{}{},
							},
							{
								Name:  "baz",
								Alias: "baz",
								UnparsedArgs: map[string]interface{}{
									"arg": float64(3),
								},
								SelectionSet: &SelectionSet{
									Selections: []*Selection{
										{
											Name:  "bah",
											Alias: "bah",
											UnparsedArgs: map[string]interface{}{
												"x": float64(1),
												"y": "123",
												"z": true,
											},
										},
										{
											Name:  "hum",
											Alias: "hum",
											UnparsedArgs: map[string]interface{}{
												"foo": map[string]interface{}{
													"x": "var value!!",
												},
												"bug": []interface{}{
													float64(1), float64(2),
													[]interface{}{float64(4), float64(5)},
												},
											},
										},
									},
								},
							},
						},
						Fragments: []*Fragment{
							{
								On: "Foo",
								SelectionSet: &SelectionSet{
									Selections: []*Selection{
										{
											Name:         "asd",
											Alias:        "asd",
											UnparsedArgs: map[string]interface{}{},
										},
									},
									Fragments: []*Fragment{
										{
											On: "Foo",
											SelectionSet: &SelectionSet{
												Selections: []*Selection{
													{
														Name:         "zxc",
														Alias:        "zxc",
														UnparsedArgs: map[string]interface{}{},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Name:         "xyz",
					Alias:        "xyz",
					UnparsedArgs: map[string]interface{}{},
				},
			},
		},
	}

	if !reflect.DeepEqual(query, expected) {
		t.Error("unexpected parse")
	}

	query, err = Parse(`
mutation foo($var: bar) {
	baz
}
`, map[string]interface{}{
		"var": "var value!!",
	})
	if err != nil {
		t.Error("unexpected error", err)
	}

	expected = &Query{
		Name: "foo",
		Kind: "mutation",
		SelectionSet: &SelectionSet{
			Selections: []*Selection{
				{
					Name:         "baz",
					Alias:        "baz",
					UnparsedArgs: map[string]interface{}{},
				},
			},
		},
	}
	if !reflect.DeepEqual(query, expected) {
		t.Error("unexpected parse")
	}
}

func TestParseUnsupported(t *testing.T) {
	_, err := Parse(``, map[string]interface{}{})
	if err == nil || err.Error() != "must have a single query" {
		t.Error("expected missing query to fail", err)
	}

	_, err = Parse(`
{
	bar
}

{
	baz
}`, map[string]interface{}{})
	if err == nil || err.Error() != "must provide operation name if query contains multiple operations" {
		t.Error("expected multiple queries to fail", err)
	}

	_, err = Parse(`
{
	b(a: 1)
	b(a: 2)
}`, map[string]interface{}{})
	if err == nil || err.Error() != "same alias with different args" {
		t.Error("expected different args to fail", err)
	}

	_, err = Parse(`
{
	a: a
	a: b
}`, map[string]interface{}{})
	if err == nil || err.Error() != "same alias with different name" {
		t.Error("expected different names to fail", err)
	}

	_, err = Parse(`
{
	a: a
	... on Foo {
		a: b
	}
}`, map[string]interface{}{})
	if err == nil || err.Error() != "same alias with different name" {
		t.Error("expected different names in fragment to fail", err)
	}

	_, err = Parse(`
{
	a(x: 1, x: 1)
}`, map[string]interface{}{})
	if err == nil || err.Error() != "duplicate arg" {
		t.Error("expected duplicate args to fail", err)
	}

	_, err = Parse(`
{
	... foo
}
fragment foo on Foo {
	... foo
}`, map[string]interface{}{})
	if err == nil || err.Error() != "fragment contains itself" {
		t.Error("expected fragment definition to fail", err)
	}

	_, err = Parse(`
{
	bar
}
fragment foo on Foo {
	x
}`, map[string]interface{}{})
	if err == nil || err.Error() != "unused fragment" {
		t.Error("expected unused fragment to fail", err)
	}
}

func TestParseRequiredVariableDefinitionWithDefaultValue(t *testing.T) {
	// A non-null variable is allowed to declare a default value: the default is
	// what makes it satisfiable without the caller supplying one.
	query, err := Parse(`
query Operation($x: Int! = 2) {
	field(x: $x)
}	`, map[string]interface{}{})
	if err != nil {
		t.Fatal("expected a non-null variable with a default to parse, but got", err)
	}

	if val := query.SelectionSet.Selections[0].UnparsedArgs["x"]; val != float64(2) {
		t.Errorf("expected 2, received %v", val)
	}
}

func TestParseFillInDefaultValues(t *testing.T) {
	// Fill in default values when provided.
	query, err := Parse(`
query Operation($x: int64 = 2) {
	field(x: $x)
}	`, map[string]interface{}{})

	if err != nil {
		t.Error("expected default value to be used, but received", err)
	}

	args := query.SelectionSet.Selections[0].UnparsedArgs

	if len := len(args); len != 1 {
		t.Errorf("expected 1 argument, received %d", len)
	}

	if val := args["x"]; val != float64(2) {
		t.Errorf("expected 2, received %v", val)
	}
}

// TestParseBlockString checks that block strings ("""...""") parse. They are a
// June 2018 addition the previous parser predated.
func TestParseBlockString(t *testing.T) {
	query, err := Parse("{ field(text: \"\"\"\nline one\nline two\n\"\"\") }", nil)
	if err != nil {
		t.Fatal("expected block string to parse, got", err)
	}

	got := query.SelectionSet.Selections[0].UnparsedArgs["text"]
	if got != "line one\nline two" {
		t.Errorf("expected the block string's common indentation to be stripped, got %q", got)
	}
}

// TestParseNullLiteral checks that the null literal parses. The previous parser
// treated `null` as a syntax error, so null could only arrive via a variable.
func TestParseNullLiteral(t *testing.T) {
	query, err := Parse(`{ field(a: null, b: {c: null}, d: [null]) }`, nil)
	if err != nil {
		t.Fatal("expected null literal to parse, got", err)
	}

	args := query.SelectionSet.Selections[0].UnparsedArgs
	if args["a"] != nil {
		t.Errorf("expected a null, got %v", args["a"])
	}
	if inner, ok := args["b"].(map[string]interface{}); !ok || len(inner) != 1 || inner["c"] != nil {
		t.Errorf("expected {c: null}, got %v", args["b"])
	}
	if list, ok := args["d"].([]interface{}); !ok || len(list) != 1 || list[0] != nil {
		t.Errorf("expected [null], got %v", args["d"])
	}
}

// TestParseSubscription checks that subscription operations parse. The previous
// parser rejected any operation that was not a query or a mutation.
func TestParseSubscription(t *testing.T) {
	query, err := Parse(`subscription Updates { counter }`, nil)
	if err != nil {
		t.Fatal("expected subscription to parse, got", err)
	}
	if query.Kind != "subscription" {
		t.Errorf("expected kind subscription, got %q", query.Kind)
	}
	if query.Name != "Updates" {
		t.Errorf("expected name Updates, got %q", query.Name)
	}
}

// TestParseOperationSelection checks operationName selection over a document
// holding several operations.
func TestParseOperationSelection(t *testing.T) {
	const source = `
query First { a }
query Second { b }
mutation Third { c }`

	for _, tt := range []struct{ name, wantKind, wantField string }{
		{"First", "query", "a"},
		{"Second", "query", "b"},
		{"Third", "mutation", "c"},
	} {
		query, err := ParseOperation(source, nil, tt.name)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tt.name, err)
		}
		if query.Kind != tt.wantKind {
			t.Errorf("%s: expected kind %q, got %q", tt.name, tt.wantKind, query.Kind)
		}
		if got := query.SelectionSet.Selections[0].Name; got != tt.wantField {
			t.Errorf("%s: expected field %q, got %q", tt.name, tt.wantField, got)
		}
	}

	if _, err := ParseOperation(source, nil, "Missing"); err == nil ||
		err.Error() != "unknown operation name: Missing" {
		t.Errorf("expected an unknown operation error, got %v", err)
	}
}

// TestParseFragmentSpreadDirectivesAreNotShared checks that a directive on one
// spread of a fragment does not leak onto its other spreads.
func TestParseFragmentSpreadDirectivesAreNotShared(t *testing.T) {
	query, err := Parse(`
{
	a { ...F }
	b { ...F @include(if: false) }
}
fragment F on Thing { x }`, nil)
	if err != nil {
		t.Fatal(err)
	}

	first := query.SelectionSet.Selections[0].SelectionSet.Fragments[0]
	second := query.SelectionSet.Selections[1].SelectionSet.Fragments[0]

	if len(first.Directives) != 0 {
		t.Errorf("expected the undecorated spread to have no directives, got %v", first.Directives)
	}
	if len(second.Directives) != 1 || second.Directives[0].Name != "include" {
		t.Errorf("expected the decorated spread to keep its directive, got %v", second.Directives)
	}
	if first.SelectionSet != second.SelectionSet {
		t.Error("expected both spreads to share the fragment's selection set")
	}
}

// TestParseInlineFragmentWithoutTypeCondition checks that a type-condition-less
// inline fragment parses. The previous parser dereferenced a nil type condition
// and panicked.
func TestParseInlineFragmentWithoutTypeCondition(t *testing.T) {
	query, err := Parse(`{ a { ... @include(if: true) { b } } }`, nil)
	if err != nil {
		t.Fatal(err)
	}

	fragments := query.SelectionSet.Selections[0].SelectionSet.Fragments
	if len(fragments) != 1 {
		t.Fatalf("expected one fragment, got %d", len(fragments))
	}
	if fragments[0].On != "" {
		t.Errorf("expected an empty type condition, got %q", fragments[0].On)
	}
	if got := fragments[0].SelectionSet.Selections[0].Name; got != "b" {
		t.Errorf("expected field b, got %q", got)
	}
}

// TestParseExplicitNullVariableBeatsDefault checks the specification's
// CoerceVariableValues rule: a variable supplied explicitly as null keeps its
// null, and only an absent variable takes the declared default.
func TestParseExplicitNullVariableBeatsDefault(t *testing.T) {
	const source = `query Op($x: Int = 7) { field(x: $x) }`

	absent, err := Parse(source, map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if got := absent.SelectionSet.Selections[0].UnparsedArgs["x"]; got != float64(7) {
		t.Errorf("expected an absent variable to take the default 7, got %v", got)
	}

	explicit, err := Parse(source, map[string]interface{}{"x": nil})
	if err != nil {
		t.Fatal(err)
	}
	if got := explicit.SelectionSet.Selections[0].UnparsedArgs["x"]; got != nil {
		t.Errorf("expected an explicit null to survive, got %v", got)
	}
}
