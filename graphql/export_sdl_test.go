package graphql_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type documentedEnum int32

const (
	documentedEnumFirst  documentedEnum = 1
	documentedEnumSecond documentedEnum = 2
)

type DocumentedThing struct {
	lightning.Meta `graphql:"DocumentedThing" description:"A thing with documentation on it."`

	Name string `description:"What the thing is called."`
	Old  string `description:"An address." deprecated:"Use addresses instead."`
	Kind documentedEnum
}

type DocumentedFilter struct {
	lightning.Meta `graphql:"DocumentedFilter"`

	Term string `description:"What to search for."`
}

type documentedSearchArgs struct {
	Filter DocumentedFilter `description:"How to narrow the search."`
}

func documentedSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	b := lightning.New()

	enum := lightning.Enum(b, "documentedEnum", map[string]documentedEnum{
		"FIRST":  documentedEnumFirst,
		"SECOND": documentedEnumSecond,
	}).Describe("How a thing is classified.")
	enum.Value("FIRST").Describe("The first kind.")
	enum.Value("SECOND").Deprecate("Nobody uses this.")

	lightning.Object[DocumentedThing](b)

	query := b.Query()
	query.FieldArgs("search", func(ctx context.Context, _ *lightning.Root, args documentedSearchArgs) (*DocumentedThing, error) {
		return nil, nil
	}).Describe("Finds a thing.")
	query.Field("legacy", func(ctx context.Context, _ *lightning.Root) (string, error) {
		return "", nil
	}).Deprecate("")

	return b.MustBuild()
}

// TestDescriptionsAndDeprecationReachSDL checks both authoring routes: struct
// tags for struct-derived fields, options for FieldFunc-registered ones.
func TestDescriptionsAndDeprecationReachSDL(t *testing.T) {
	sdl, err := graphql.PrintSchema(documentedSchema(t))
	require.NoError(t, err)

	for _, want := range []string{
		"A thing with documentation on it.",
		"What the thing is called.",
		`old: String! @deprecated(reason: "Use addresses instead.")`,
		"How a thing is classified.",
		"The first kind.",
		`SECOND @deprecated(reason: "Nobody uses this.")`,
		"Finds a thing.",
		"How to narrow the search.",
		`legacy: String! @deprecated(reason: "No longer supported")`,
		"What to search for.",
	} {
		require.Contains(t, sdl, want, "printed schema:\n%s", sdl)
	}
}

// TestExportedSDLRoundTrips is Phase 8's acceptance criterion: the exported SDL
// parses back with gqlparser and matches what introspection reports.
func TestExportedSDLRoundTrips(t *testing.T) {
	built := documentedSchema(t)

	sdl, err := graphql.PrintSchema(built)
	require.NoError(t, err)

	reparsed, err := graphql.ASTSchema(built)
	require.NoError(t, err)

	// Every type the printer emitted is in the reparsed schema, with the same
	// fields and the same documentation.
	thing := reparsed.Types["DocumentedThing"]
	require.NotNil(t, thing, "printed schema:\n%s", sdl)
	require.Equal(t, ast.Object, thing.Kind)
	require.Equal(t, "A thing with documentation on it.", thing.Description)
	require.Equal(t, "What the thing is called.", thing.Fields.ForName("name").Description)

	old := thing.Fields.ForName("old")
	require.NotNil(t, old.Directives.ForName("deprecated"))

	kind := reparsed.Types["documentedEnum"]
	require.NotNil(t, kind)
	require.Equal(t, ast.Enum, kind.Kind)
	require.Equal(t, "How a thing is classified.", kind.Description)
	require.Equal(t, "The first kind.", kind.EnumValues.ForName("FIRST").Description)
	require.NotNil(t, kind.EnumValues.ForName("SECOND").Directives.ForName("deprecated"))

	// Printing the schema twice gives the same bytes, so the file can be
	// committed and diffed.
	again, err := graphql.PrintSchema(built)
	require.NoError(t, err)
	require.Equal(t, sdl, again)
}

// TestWriteSchemaFile checks the build-step helper.
func TestWriteSchemaFile(t *testing.T) {
	built := documentedSchema(t)
	path := filepath.Join(t.TempDir(), "generated", "schema.graphql")

	require.NoError(t, graphql.WriteSchemaFile(built, path))

	written, err := os.ReadFile(path)
	require.NoError(t, err)

	expected, err := graphql.PrintSchema(built)
	require.NoError(t, err)
	require.Equal(t, expected, string(written))

	// Writing again over an existing file works.
	require.NoError(t, graphql.WriteSchemaFile(built, path))

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary files should be left behind")
}
