package graphql_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/internal"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeQuery(onArgParse *func()) *graphql.Object {
	noArguments := func(json interface{}) (interface{}, error) {
		return nil, nil
	}

	query := &graphql.Object{
		Name:   "Query",
		Fields: make(map[string]*graphql.Field),
	}

	a := &graphql.Object{
		Name: "A",
		KeyField: &graphql.Field{
			Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
				return source, nil
			},
			Type: &graphql.Scalar{Type: "string"},
		},
		Fields: make(map[string]*graphql.Field),
	}

	query.Fields["a"] = &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			return 0, nil
		},
		Type:           a,
		ParseArguments: noArguments,
	}

	query.Fields["as"] = &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			return []int{0, 1, 2, 3}, nil
		},
		Type:           &graphql.List{Type: a},
		ParseArguments: noArguments,
	}

	query.Fields["static"] = &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			return "static", nil
		},
		Type:           &graphql.Scalar{Type: "string"},
		ParseArguments: noArguments,
	}

	query.Fields["error"] = &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			return nil, errors.New("test error")
		},
		Type:           &graphql.Scalar{Type: "string"},
		ParseArguments: noArguments,
	}

	query.Fields["panic"] = &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			panic("test panic")
		},
		Type:           &graphql.Scalar{Type: "string"},
		ParseArguments: noArguments,
	}

	a.Fields["value"] = &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			return source.(int), nil
		},
		Type:           &graphql.Scalar{Type: "int"},
		ParseArguments: noArguments,
	}

	a.Fields["valuePtr"] = &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			temp := source.(int)
			if temp%2 == 0 {
				return nil, nil
			}
			return &temp, nil
		},
		Type:           &graphql.Scalar{Type: "int"},
		ParseArguments: noArguments,
	}

	a.Fields["nested"] = &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			return source.(int) + 1, nil
		},
		Type:           a,
		ParseArguments: noArguments,
	}

	a.Fields["fieldWithArgs"] = &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			return 1, nil
		},
		Type: &graphql.Scalar{Type: "int"},
		ParseArguments: func(json interface{}) (interface{}, error) {
			if onArgParse != nil {
				(*onArgParse)()
			}
			return nil, nil
		},
	}

	return query
}

func TestBasic(t *testing.T) {
	query := makeQuery(nil)

	q := graphql.MustParse(`{
		static
		a { value nested { value } }
		as { value valuePtr }
	}`, nil)

	if err := graphql.PrepareQuery(context.Background(), query, q.SelectionSet); err != nil {
		t.Error(err)
	}
	e := testgraphql.NewExecutorWrapper(t)
	result, err := e.Execute(context.Background(), query, nil, q)
	if err != nil {
		t.Error(err)
	}

	// assert that result["as"][1]["valuePtr"] == 1 (and not a pointer to 1)
	root, _ := internal.AsJSON(result).(map[string]interface{})
	as, _ := root["as"].([]interface{})
	asObject, _ := as[1].(map[string]interface{})
	if int(asObject["valuePtr"].(float64)) != 1 {
		t.Error("Expected valuePtr to be 1, was", asObject["valuePtr"])
	}

	if !reflect.DeepEqual(internal.AsJSON(result), internal.ParseJSON(`
{
	"static": "static",
	"a": {
		"value": 0,
		"__key": 0,
		"nested": {
			"value": 1,
			"__key": 1
		}
	},
	"as": [
		{"value": 0, "valuePtr": null, "__key": 0},
		{"value": 1, "valuePtr": 1, "__key": 1},
		{"value": 2, "valuePtr": null, "__key": 2},
		{"value": 3, "valuePtr": 3, "__key": 3}
	]
}`)) {
		t.Errorf("bad value: %s", internal.MarshalJSON(internal.AsJSON(result)))
	}
}

func TestRepeatedFragment(t *testing.T) {
	ctr := 0
	countArgParse := func() {
		ctr++
	}
	query := makeQuery(&countArgParse)

	q := graphql.MustParse(`{
		static
		a { value nested { value ...frag } ...frag }
		as { value }
	}
	fragment frag on A {
		fieldWithArgs(arg1: 1)
	}
	`, nil)

	if err := graphql.PrepareQuery(context.Background(), query, q.SelectionSet); err != nil {
		t.Error(err)
	}
	e := testgraphql.NewExecutorWrapper(t)
	_, err := e.Execute(context.Background(), query, nil, q)
	if err != nil {
		t.Error(err)
	}

	if ctr != 1 {
		t.Errorf("Expected args for fragment to be parsed once, but they were parsed %d times.", ctr)
	}
}

func TestFlatten(t *testing.T) {
	type Args struct {
		Value int
	}
	result, err := graphql.FlattenAll(&graphql.SelectionSet{
		Selections: []*graphql.Selection{
			{
				Name:  "a",
				Alias: "b",
				Args: &Args{
					Value: 2,
				},
				ParentType: "Query",
				SelectionSet: &graphql.SelectionSet{
					Selections: []*graphql.Selection{{
						Name:         "foo",
						UnparsedArgs: map[string]interface{}{},
						ParentType:   "A",
					}},
				},
			},
			{
				Name:  "a",
				Alias: "b",
				Args: &Args{
					Value: 2,
				},
				ParentType: "Query",
				SelectionSet: &graphql.SelectionSet{
					Selections: []*graphql.Selection{{
						Name:         "foo",
						UnparsedArgs: map[string]interface{}{},
						ParentType:   "A",
					}},
				},
			},
		},
	})
	assert.NoError(t, err)

	assert.Equal(t,
		[]*graphql.Selection{
			{
				Name:  "a",
				Alias: "b",
				Args: &Args{
					Value: 2,
				},
				ParentType: "Query",
				SelectionSet: &graphql.SelectionSet{
					// Flatten needs to be run on every level. On a subsequent run,
					// these foo's would also get flattened.
					Selections: []*graphql.Selection{{
						Name:         "foo",
						UnparsedArgs: map[string]interface{}{},
						ParentType:   "A",
					}, {
						Name:         "foo",
						UnparsedArgs: map[string]interface{}{},
						ParentType:   "A",
					}},
				},
			},
		}, result,
	)
}

/*
func TestMissingField(t *testing.T) {
	q := MustParse(`
		{
			unknown
		}
	`, map[string]interface{}{})

	if err := PrepareQuery(query, q); err == nil {
		t.Error("expected error")
	}
}

func TestMissingSelectors(t *testing.T) {
	q := MustParse(`
		{
			nested
		}
	`, map[string]interface{}{})

	if err := PrepareQuery(query, q); err == nil {
		t.Error("expected error")
	}
}

func TestUnwantedSelectors(t *testing.T) {
	q := MustParse(`
		{
			bar { bar }
		}
	`, map[string]interface{}{})

	if err := PrepareQuery(query, q); err == nil {
		t.Error("expected error")
	}
}

func TestBadArgs(t *testing.T) {
	q := MustParse(`
		{
			sum(a: "123", b: 4)
		}
	`, map[string]interface{}{})

	if err := PrepareQuery(query, q); err == nil {
		t.Error("expected error")
	}
}
*/

func TestError(t *testing.T) {
	query := makeQuery(nil)

	q := graphql.MustParse(`
		query foo {
			error
		}
	`, map[string]interface{}{})

	if err := graphql.PrepareQuery(context.Background(), query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e := testgraphql.NewExecutorWrapper(t)
	_, err := e.Execute(context.Background(), query, nil, q)
	// The path starts at the root field. An operation name is not part of a
	// response path.
	if err == nil || err.Error() != "error: test error" {
		t.Errorf("expected test error, got %v", err)
	}
}

// TestPanic tests that a panicing resolver will report an error to a
// context implementing PanicReporter instead of crashing the server.
func TestPanic(t *testing.T) {
	query := makeQuery(nil)

	q := graphql.MustParse(`
		{
			panic
		}
	`, nil)

	if err := graphql.PrepareQuery(context.Background(), query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e := testgraphql.NewExecutorWrapperWithoutExactErrorMatch(t)
	_, err := e.Execute(context.Background(), query, nil, q)
	if err == nil || !strings.Contains(err.Error(), "test panic") {
		t.Error("expected test panic")
	}
	if !strings.Contains(err.Error(), "executor_test.go") {
		t.Error("expected stacktrace")
	}
}

func TestSelectionType(t *testing.T) {
	query := makeQuery(nil)

	q := graphql.MustParse(`
		{
			a {
				fieldWithArgs(arg: 1)
			}
		}
	`, nil)

	if err := graphql.PrepareQuery(context.Background(), query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e := testgraphql.NewExecutorWrapperWithoutExactErrorMatch(t)
	_, err := e.Execute(context.Background(), query, nil, q)
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"arg": float64(1)}, q.SelectionSet.Selections[0].SelectionSet.Selections[0].UnparsedArgs)
	assert.Equal(t, "A", q.SelectionSet.Selections[0].SelectionSet.Selections[0].ParentType)
}

// TODO: Verify caching and concurrency

// executorObject is the element of the list the failure cases walk.
type executorObject struct {
	lightning.Meta `graphql:"object"`

	Key string
}

func TestExecutorRuns(t *testing.T) {
	tests := []struct {
		name           string
		objectFunc     func(context.Context, *lightning.Root) ([]*executorObject, error)
		resolverFunc   func(context.Context, *executorObject) (string, error)
		query          string
		wantResultJSON string
		wantError      string
	}{
		{
			name: "fail on 3rd value",
			objectFunc: func(ctx context.Context, _ *lightning.Root) ([]*executorObject, error) {
				return []*executorObject{
					{Key: "key1"},
					{Key: "key2"},
					{Key: "key3"},
				}, nil
			},
			resolverFunc: func(ctx context.Context, o *executorObject) (string, error) {
				if o.Key == "key3" {
					return "", errors.New("failing on third key")
				}
				return o.Key, nil
			},
			query: `
			{
				objects {
					key
					value
				}
			}`,
			wantError: "objects.2.value: failing on third key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := lightning.New()
			lightning.Object[executorObject](b).Field("value", tt.resolverFunc)
			b.Query().Field("objects", tt.objectFunc)

			schema, err := b.Build()
			require.NoError(t, err)

			q := graphql.MustParse(tt.query, nil)

			if err := graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet); err != nil {
				t.Error(err)
			}

			e := testgraphql.NewExecutorWrapper(t)

			ctx := context.Background()
			res, err := e.Execute(ctx, schema.Query, nil, q)
			if tt.wantError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantError)
				return
			}
			require.NoError(t, err)

			wantParsedJSON := internal.ParseJSON(tt.wantResultJSON)
			gotJSON := internal.AsJSON(res)

			require.Equal(
				t,
				wantParsedJSON,
				gotJSON,
				"Mismatch for expected vs actual response.  Want:\n%s\nGot:\n%s",
				internal.MarshalJSON(wantParsedJSON),
				internal.MarshalJSON(gotJSON),
			)
		})
	}
}

func Test_pathError_Reason(t *testing.T) {
	type fields struct {
		inner error
		path  []interface{}
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "empty list",
			fields: fields{
				inner: nil,
				path:  []interface{}{},
			},
			want: "",
		},
		{
			name: "non empty list",
			fields: fields{
				inner: fmt.Errorf("error"),
				path:  []interface{}{"a", "b", "c"},
			},
			want: "c.b.a",
		},
		{
			name: "list indices render as numbers",
			fields: fields{
				inner: fmt.Errorf("error"),
				path:  []interface{}{"name", 2, "users"},
			},
			want: "users.2.name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pe := graphql.PathErrorInit(tt.fields.inner, tt.fields.path).(*graphql.PathError)
			if got := pe.Reason(); got != tt.want {
				t.Errorf("Reason() = %v, want %v", got, tt.want)
			}
		})
	}
}
