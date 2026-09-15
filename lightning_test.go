package lightning_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/stretchr/testify/require"
)

// Task is a plain data type. Everything the schema knows about it is written
// here, next to the thing it describes.
type Task struct {
	lightning.Meta `graphql:"Task" description:"A unit of work."`

	Key   string  `graphql:"-"`
	Title string  `description:"What needs doing."`
	Done  bool    `description:"Whether it has been done."`
	Notes *string `description:"Anything else worth saying."`
}

type User struct {
	lightning.Meta `description:"A person."`

	Key  string `graphql:"-"`
	Name string `description:"Their display name."`
}

// run builds a schema, executes a query against it, and returns the result as
// JSON-comparable data.
func run(t *testing.T, schema *graphql.Schema, query string) map[string]any {
	t.Helper()

	q, err := graphql.Parse(query, nil)
	require.NoError(t, err)
	require.NoError(t, graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet))

	e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
	result, err := e.Execute(context.Background(), schema.Query, nil, q)
	require.NoError(t, err)

	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(encoded, &out))
	return out
}

// TestPlainTypeNeedsNoFieldDeclarations is the headline claim: a struct is
// already a complete description of itself.
func TestPlainTypeNeedsNoFieldDeclarations(t *testing.T) {
	b := lightning.New()
	lightning.Object[Task](b)

	b.Query().Field("task", func(ctx context.Context, _ *lightning.Root) (*Task, error) {
		return &Task{Key: "t1", Title: "Write the schema", Done: true}, nil
	})

	schema := b.MustBuild()

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)

	// The description came from the marker, the field docs from the fields, and
	// nullability from the Go types.
	require.Contains(t, sdl, "A unit of work.")
	require.Contains(t, sdl, "What needs doing.")
	require.Contains(t, sdl, "title: String!")
	require.Contains(t, sdl, "done: Boolean!")
	require.Contains(t, sdl, "notes: String\n", "a *string field is nullable")
	require.NotContains(t, sdl, "key:", "a graphql:\"-\" field is not exposed")

	got := run(t, schema, `{ task { title done } }`)
	require.Equal(t, map[string]any{"task": map[string]any{"title": "Write the schema", "done": true}}, got)
}

// TestNullabilityFollowsTheGoType covers the rule at every level, including the
// list case this library used to get wrong.
func TestNullabilityFollowsTheGoType(t *testing.T) {
	type Shapes struct {
		Plain    string    `description:"non-null"`
		Pointer  *string   `description:"nullable"`
		List     []string  `description:"non-null list of non-null"`
		PtrElems []*string `description:"non-null list of nullable"`
		PtrList  *[]string `description:"nullable list of non-null"`
	}

	b := lightning.New()
	lightning.Object[Shapes](b)
	b.Query().Field("shapes", func(ctx context.Context, _ *lightning.Root) (*Shapes, error) {
		return &Shapes{}, nil
	})

	sdl, err := graphql.PrintSchema(b.MustBuild())
	require.NoError(t, err)

	for _, want := range []string{
		"plain: String!",
		"pointer: String",
		"list: [String!]!",
		"ptrElems: [String]!",
		"ptrList: [String!]",
	} {
		require.Contains(t, sdl, want, "printed schema:\n%s", sdl)
	}
}

// TestFieldTypeComesFromTheResolver checks that no call site names a type.
func TestFieldTypeComesFromTheResolver(t *testing.T) {
	b := lightning.New()
	user := lightning.Object[User](b)
	lightning.Object[Task](b)

	// A nullable object, a non-null list of nullable objects, and a scalar —
	// each declared by nothing but its resolver's signature.
	user.Field("topTask", func(ctx context.Context, u *User) (*Task, error) {
		return &Task{Title: "top"}, nil
	})
	user.Field("tasks", func(ctx context.Context, u *User) ([]*Task, error) {
		return []*Task{{Title: "one"}, {Title: "two"}}, nil
	})
	user.Attr("shoutName", func(u *User) string { return strings.ToUpper(u.Name) })

	b.Query().Field("viewer", func(ctx context.Context, _ *lightning.Root) (*User, error) {
		return &User{Key: "u1", Name: "Ada"}, nil
	})

	schema := b.MustBuild()

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	require.Contains(t, sdl, "topTask: Task\n")
	require.Contains(t, sdl, "tasks: [Task]!")
	require.Contains(t, sdl, "shoutName: String!")

	got := run(t, schema, `{ viewer { name shoutName topTask { title } tasks { title } } }`)
	viewer := got["viewer"].(map[string]any)
	require.Equal(t, "Ada", viewer["name"])
	require.Equal(t, "ADA", viewer["shoutName"])
	require.Equal(t, map[string]any{"title": "top"}, viewer["topTask"])
	require.Len(t, viewer["tasks"], 2)
}

// TestUnregisteredTypeIsNamed checks that a forgotten declaration says which
// type was forgotten.
func TestUnregisteredTypeIsNamed(t *testing.T) {
	b := lightning.New()
	b.Query().Field("task", func(ctx context.Context, _ *lightning.Root) (*Task, error) {
		return nil, nil
	})

	_, err := b.Build()
	require.Error(t, err)
	require.Contains(t, err.Error(), "Task is not registered")
	require.Contains(t, err.Error(), "lightning.Object")
}

// TestMisspelledTagIsReported checks that a tag that documents nothing is an
// error rather than silence.
func TestMisspelledTagIsReported(t *testing.T) {
	type Typo struct {
		Name string `describe:"this key is wrong"`
	}

	b := lightning.New()
	lightning.Object[Typo](b)
	b.Query().Field("typo", func(ctx context.Context, _ *lightning.Root) (*Typo, error) {
		return nil, nil
	})

	_, err := b.Build()
	require.Error(t, err)
	require.Contains(t, err.Error(), `"describe"`)
	require.Contains(t, err.Error(), `did you mean "description"`)
}

// TestFieldNameDerivation checks the acronym rule that replaces the old
// lower-the-first-letter mangling.
func TestFieldNameDerivation(t *testing.T) {
	type Names struct {
		Title      string
		OwnerID    string
		ID         string
		HTTPServer string
		URL        string
	}

	b := lightning.New()
	lightning.Object[Names](b)
	b.Query().Field("names", func(ctx context.Context, _ *lightning.Root) (*Names, error) {
		return nil, nil
	})

	sdl, err := graphql.PrintSchema(b.MustBuild())
	require.NoError(t, err)

	for _, want := range []string{"title:", "ownerId:", "id:", "httpServer:", "url:"} {
		require.Contains(t, sdl, want, "printed schema:\n%s", sdl)
	}
	require.NotContains(t, sdl, "ownerID:", "OwnerID used to become ownerID, which is neither Go's spelling nor GraphQL's")
}

// TestFieldNameSpellings pins the acronym rule down case by case.
func TestFieldNameSpellings(t *testing.T) {
	type Spellings struct {
		Title      string
		OwnerID    string
		ID         string
		HTTPServer string
		URL        string
		UserURL    string
		A          string
		IDs        string
	}

	b := lightning.New()
	lightning.Object[Spellings](b)
	b.Query().Field("s", func(ctx context.Context, _ *lightning.Root) (*Spellings, error) { return nil, nil })

	sdl, err := graphql.PrintSchema(b.MustBuild())
	require.NoError(t, err)

	for _, want := range []string{
		"title: String!", "ownerId: String!", "id: String!",
		"httpServer: String!", "url: String!", "userUrl: String!",
		"a: String!", "iDs: String!",
	} {
		require.Contains(t, sdl, want, "printed schema:\n%s", sdl)
	}
}

// runErr executes a query and returns its error rather than failing the test,
// for the cases that are about what goes wrong.
func runErr(t *testing.T, schema *graphql.Schema, query string) (any, error) {
	t.Helper()

	q, err := graphql.Parse(query, nil)
	require.NoError(t, err)
	require.NoError(t, graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet))

	e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
	return e.Execute(context.Background(), schema.Query, nil, q)
}

// printSchema renders a built schema as SDL.
func printSchema(t *testing.T, schema *graphql.Schema) string {
	t.Helper()

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	return sdl
}
