package lightning_test

import (
	"context"
	"testing"
	"time"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/stretchr/testify/require"
)

// SearchArgs is an argument struct. Everything each argument has to say about
// itself is written on the argument: its name, its documentation, its default,
// and whether it is required.
type SearchArgs struct {
	lightning.Meta `graphql:"SearchInput" description:"How to narrow a search."`

	Term    string   `description:"What to look for."`
	Limit   *int32   `description:"How many to return." default:"20"`
	Tags    []string `description:"Only these tags."`
	OwnerID string   `graphql:"ownerId" description:"Whose tasks to search."`
}

// Status is an enum backed by a Go type.
type Status int32

const (
	StatusTodo Status = iota
	StatusDone
)

func statusSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	b := lightning.New()

	lightning.Enum(b, "Status", map[string]Status{
		"TODO": StatusTodo,
		"DONE": StatusDone,
	}).Describe("How far along a task is.").
		Value("TODO").Describe("Nobody has finished it yet.")

	lightning.Object[Task](b)

	q := b.Query()
	q.FieldArgs("search", func(ctx context.Context, _ *lightning.Root, args SearchArgs) ([]*Task, error) {
		limit := int32(0)
		if args.Limit != nil {
			limit = *args.Limit
		}
		return []*Task{{Title: args.Term, Done: limit > 10}}, nil
	}).Describe("Finds tasks.")

	q.FieldArgs("byStatus", func(ctx context.Context, _ *lightning.Root, args struct {
		Status Status `description:"Which status to match."`
	}) (*Task, error) {
		return &Task{Title: "matched", Done: args.Status == StatusDone}, nil
	})

	return b.MustBuild()
}

// TestArgumentsComeFromTheStruct is the headline: the argument type never
// appears at the call site, and the struct carries the documentation.
func TestArgumentsComeFromTheStruct(t *testing.T) {
	schema := statusSchema(t)

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)

	for _, want := range []string{
		"What to look for.",
		"How many to return.",
		"Whose tasks to search.",
		"term: String!",
		"limit: Int",
		"tags: [String!]!",
		"ownerId: String!",
	} {
		require.Contains(t, sdl, want, "printed schema:\n%s", sdl)
	}

	got := run(t, schema, `{ search(term: "write", ownerId: "u1", tags: ["a"], limit: 30) { title done } }`)
	tasks := got["search"].([]any)
	require.Len(t, tasks, 1)
	require.Equal(t, map[string]any{"title": "write", "done": true}, tasks[0])
}

// TestArgumentDefaultApplies checks that an omitted argument takes its tag's
// default, and that the default makes it optional.
func TestArgumentDefaultApplies(t *testing.T) {
	schema := statusSchema(t)

	// limit is omitted, so it defaults to 20, which is not greater than 10.
	got := run(t, schema, `{ search(term: "write", ownerId: "u1", tags: []) { title done } }`)
	tasks := got["search"].([]any)
	require.Equal(t, map[string]any{"title": "write", "done": true}, tasks[0])
}

// TestRequiredArgumentIsRequired checks that a value-typed argument must be
// supplied.
func TestRequiredArgumentIsRequired(t *testing.T) {
	schema := statusSchema(t)

	q, err := graphql.Parse(`{ search(tags: []) { title } }`, nil)
	require.NoError(t, err)
	err = graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "term")
}

// TestUnknownArgumentIsRejected checks that a misspelled argument is an error
// rather than being ignored.
func TestUnknownArgumentIsRejected(t *testing.T) {
	schema := statusSchema(t)

	q, err := graphql.Parse(`{ search(term: "x", ownerId: "u1", tags: [], limitt: 5) { title } }`, nil)
	require.NoError(t, err)
	err = graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "limitt")
}

// TestEnum covers an enum used as both an output type and an argument.
func TestEnum(t *testing.T) {
	schema := statusSchema(t)

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	require.Contains(t, sdl, "enum Status {")
	require.Contains(t, sdl, "How far along a task is.")
	require.Contains(t, sdl, "Nobody has finished it yet.")
	require.Contains(t, sdl, "status: Status!")

	got := run(t, schema, `{ byStatus(status: DONE) { title done } }`)
	require.Equal(t, map[string]any{"byStatus": map[string]any{"title": "matched", "done": true}}, got)
}

// TestInlineArgsStruct checks that an anonymous struct works for a one-off
// argument list, which is the shortest thing that can be written.
func TestInlineArgsStruct(t *testing.T) {
	b := lightning.New()
	lightning.Object[Task](b)

	b.Query().FieldArgs("echo", func(ctx context.Context, _ *lightning.Root, args struct {
		Text string `description:"What to say back."`
	}) (*Task, error) {
		return &Task{Title: args.Text}, nil
	})

	schema := b.MustBuild()
	got := run(t, schema, `{ echo(text: "hello") { title } }`)
	require.Equal(t, map[string]any{"echo": map[string]any{"title": "hello"}}, got)
}

// TestNestedInputObject checks that a struct-valued argument becomes an input
// object type.
func TestNestedInputObject(t *testing.T) {
	type Filter struct {
		lightning.Meta `graphql:"TaskFilter" description:"Narrows a task list."`

		Done *bool  `description:"Only tasks in this state."`
		Text string `description:"Only tasks whose title contains this."`
	}

	b := lightning.New()
	lightning.Object[Task](b)

	b.Query().FieldArgs("filtered", func(ctx context.Context, _ *lightning.Root, args struct {
		Filter Filter `description:"How to narrow the list."`
	}) ([]*Task, error) {
		return []*Task{{Title: args.Filter.Text, Done: args.Filter.Done != nil && *args.Filter.Done}}, nil
	})

	schema := b.MustBuild()

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	require.Contains(t, sdl, "input TaskFilter {")
	require.Contains(t, sdl, "Narrows a task list.")
	require.Contains(t, sdl, "filter: TaskFilter!")

	got := run(t, schema, `{ filtered(filter: {text: "write", done: true}) { title done } }`)
	tasks := got["filtered"].([]any)
	require.Equal(t, map[string]any{"title": "write", "done": true}, tasks[0])
}

// TestIntArgumentIsBounded checks that an argument that does not fit its Go
// type is refused rather than truncated.
func TestIntArgumentIsBounded(t *testing.T) {
	b := lightning.New()
	lightning.Object[Task](b)
	b.Query().FieldArgs("small", func(ctx context.Context, _ *lightning.Root, args struct {
		V int32
	}) (*Task, error) {
		return &Task{}, nil
	})
	schema := b.MustBuild()

	q, err := graphql.Parse(`{ small(v: 2147483648) { title } }`, nil)
	require.NoError(t, err)
	err = graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet)
	require.Error(t, err)
	require.Contains(t, err.Error(), "32-bit signed integer")
}

// ArgObject is declared as an output object, which is what makes it unusable as
// an argument.
type ArgObject struct {
	lightning.Meta `graphql:"ArgObject"`

	Name string
}

// TestAnOutputObjectCannotBeAnArgument catches a schema that would be illegal
// in a way only a client would discover.
func TestAnOutputObjectCannotBeAnArgument(t *testing.T) {
	type LookArgs struct {
		Thing ArgObject
	}

	b := lightning.New()
	lightning.Object[ArgObject](b)
	b.Query().FieldArgs("look", func(ctx context.Context, _ *lightning.Root, args LookArgs) (string, error) {
		return "", nil
	})

	_, err := b.Build()
	require.ErrorContains(t, err, "is declared as an object type, so it cannot also be an argument")
}

// everyArgs has one argument of every scalar kind, so that each parser is
// exercised by a real query rather than only by the types it maps to.
type everyArgs struct {
	Text    string
	Flag    bool
	Small   int32
	Wide    int64
	Big     uint64
	Ratio   float64
	Ident   lightning.ID
	When    time.Time
	Raw     []byte
	Words   []string
	Missing *string
}

// TestEveryScalarArgumentParses covers the input side of the scalar mapping.
// The output side is checked elsewhere; this is the half a client can get
// wrong.
func TestEveryScalarArgumentParses(t *testing.T) {
	b := lightning.New()
	b.Query().FieldArgs("echo", func(ctx context.Context, _ *lightning.Root, args everyArgs) (string, error) {
		require.Equal(t, "hello", args.Text)
		require.Equal(t, true, args.Flag)
		require.Equal(t, int32(-7), args.Small)
		require.Equal(t, int64(9223372036854775807), args.Wide)
		require.Equal(t, uint64(18446744073709551615), args.Big)
		require.Equal(t, 1.5, args.Ratio)
		require.Equal(t, "abc", args.Ident.Value)
		require.Equal(t, time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), args.When.UTC())
		require.Equal(t, []byte("bar"), args.Raw)
		require.Equal(t, []string{"one", "two"}, args.Words)
		require.Nil(t, args.Missing)
		return "ok", nil
	})

	schema := b.MustBuild()

	sdl := printSchema(t, schema)
	require.Contains(t, sdl, "when: Time!")
	require.Contains(t, sdl, "raw: Bytes!")
	require.Contains(t, sdl, "words: [String!]!")
	// A pointer is the only optional one.
	require.Contains(t, sdl, "missing: String,")

	got := run(t, schema, `{
		echo(
			text: "hello"
			flag: true
			small: -7
			wide: "9223372036854775807"
			big: "18446744073709551615"
			ratio: 1.5
			ident: "abc"
			when: "2020-01-02T03:04:05Z"
			raw: "YmFy"
			words: ["one", "two"]
		)
	}`)
	require.Equal(t, "ok", got["echo"])
}

// TestBadScalarArgumentsAreReported checks that each parser says what was wrong
// rather than failing obscurely.
func TestBadScalarArgumentsAreReported(t *testing.T) {
	type oneArg[T any] struct{ V T }

	for _, tc := range []struct {
		name   string
		build  func(*lightning.Builder)
		query  string
		expect string
	}{
		{
			name: "a Time that is not one",
			build: func(b *lightning.Builder) {
				b.Query().FieldArgs("v", func(ctx context.Context, _ *lightning.Root, a oneArg[time.Time]) (bool, error) {
					return true, nil
				})
			},
			query:  `{ v(v: "yesterday") }`,
			expect: "not an RFC 3339 timestamp",
		},
		{
			name: "Bytes that are not base64",
			build: func(b *lightning.Builder) {
				b.Query().FieldArgs("v", func(ctx context.Context, _ *lightning.Root, a oneArg[[]byte]) (bool, error) {
					return true, nil
				})
			},
			query:  `{ v(v: "not base64 at all!!") }`,
			expect: "base64",
		},
		{
			name: "a negative unsigned integer",
			build: func(b *lightning.Builder) {
				b.Query().FieldArgs("v", func(ctx context.Context, _ *lightning.Root, a oneArg[uint64]) (bool, error) {
					return true, nil
				})
			},
			query:  `{ v(v: "-1") }`,
			expect: "Int64",
		},
		{
			name: "a Float that is a word",
			build: func(b *lightning.Builder) {
				b.Query().FieldArgs("v", func(ctx context.Context, _ *lightning.Root, a oneArg[float64]) (bool, error) {
					return true, nil
				})
			},
			query:  `{ v(v: "half") }`,
			expect: "expected a number",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := lightning.New()
			tc.build(b)
			schema := b.MustBuild()

			q, err := graphql.Parse(tc.query, nil)
			if err == nil {
				err = graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet)
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expect)
		})
	}
}
