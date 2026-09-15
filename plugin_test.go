package lightning_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/stretchr/testify/require"
)

// This file is the plugin seam's acceptance criterion: a plugin written outside
// the lightning package, using only its exported API, that contributes a type
// and a root field, wraps every resolver, annotates what it sees, and refuses a
// schema it does not like.
//
// It lives in the external test package on purpose. If any of it needed
// unexported access, the seam would be wrong.

// stamp is a plugin that shouts every string a schema resolves.
type stamp struct {
	// open and close are wrapped around each string result, so two stamps
	// compose visibly and in declaration order.
	open, close string
	// rootField is the name of the root field this instance contributes.
	rootField string

	// seen records the fields the plugin was shown, to prove it sees them all.
	seen []string
	// calls counts how often a wrapped resolver ran.
	calls atomic.Int32
}

func (s *stamp) PluginName() string { return "stamp:" + s.open }

// Install contributes a type of the plugin's own.
func (s *stamp) Install(b *lightning.Builder) error {
	lightning.Object[PluginInfo](b)
	return nil
}

// Field wraps every field's resolver, and remembers what it was shown.
func (s *stamp) Field(b *lightning.Builder, typeName string, info lightning.FieldInfo, field *graphql.Field) error {
	s.seen = append(s.seen, typeName+"."+info.Name())

	if quiet, _ := info.Meta("stamp.quiet"); quiet == true {
		return nil
	}

	inner := field.Resolve
	field.Resolve = func(ctx context.Context, source, args any, sel *graphql.SelectionSet) (any, error) {
		s.calls.Add(1)
		result, err := inner(ctx, source, args, sel)
		if text, ok := result.(string); ok {
			return s.open + text + s.close, nil
		}
		return result, err
	}
	return nil
}

// BeforeBuild adds a root field assembled from what the application declared.
func (s *stamp) BeforeBuild(b *lightning.Builder) error {
	names := make([]string, 0, len(b.DeclaredTypes()))
	for _, declared := range b.DeclaredTypes() {
		if declared.IsObject() {
			names = append(names, declared.Name())
			declared.SetMeta("stamp.stamped", true)
		}
	}

	// The plugin's own type, built through the seam so the root field it adds
	// can return it.
	built, err := b.BuiltType(reflect.TypeFor[PluginInfo]())
	if err != nil {
		return err
	}

	b.RootField(lightning.QueryRoot, s.rootField, &graphql.Field{
		Type:        built,
		Description: "What the stamp plugin saw.",
		ParseArguments: func(any) (any, error) {
			return nil, nil
		},
		Resolve: func(ctx context.Context, source, args any, sel *graphql.SelectionSet) (any, error) {
			return &PluginInfo{Note: strings.Join(names, ",")}, nil
		},
	})
	return nil
}

// AfterBuild inspects the finished schema.
func (s *stamp) AfterBuild(b *lightning.Builder, schema *graphql.Schema) error {
	if _, ok := schema.Query.(*graphql.Object).Fields[s.rootField]; !ok {
		return fmt.Errorf("the root field this plugin added is missing")
	}
	return nil
}

// PluginInfo is the type the plugin contributes, to prove it can.
type PluginInfo struct {
	lightning.Meta `graphql:"PluginInfo" description:"Contributed by a plugin."`

	Note string `description:"Every object type the plugin saw."`
}

// picky refuses any schema whose query root has a field it dislikes.
type picky struct{ forbidden string }

func (p *picky) PluginName() string { return "picky" }

func (p *picky) BeforeBuild(b *lightning.Builder) error {
	for _, declared := range b.DeclaredTypes() {
		if declared.HasField(p.forbidden) {
			b.Errorf("%s declares %s, which this schema does not allow", declared.Name(), p.forbidden)
		}
	}
	return nil
}

// TestPluginSeam is the acceptance criterion, in one test.
func TestPluginSeam(t *testing.T) {
	first := &stamp{open: "<", close: ">", rootField: "pluginInfo"}
	second := &stamp{open: "[", close: "]", rootField: "otherPluginInfo"}

	b := lightning.New(first, second)

	task := lightning.Object[Task](b)
	task.Attr("shout", func(t *Task) string { return t.Title })
	task.Attr("quiet", func(t *Task) string { return t.Title }).Meta("stamp.quiet", true)

	b.Query().Field("task", func(ctx context.Context, _ *lightning.Root) (*Task, error) {
		return &Task{Key: "t1", Title: "One"}, nil
	})

	schema := b.MustBuild()

	// The type the plugin contributed is in the schema, described by its own
	// struct tag.
	sdl := printSchema(t, schema)
	require.Contains(t, sdl, "Contributed by a plugin.")

	got := run(t, schema, `{ pluginInfo { note } task { title shout quiet } }`)

	// The root field the plugin assembled sees every declared object.
	names := got["pluginInfo"].(map[string]any)["note"].(string)
	require.Contains(t, names, "Task")
	require.Contains(t, names, "PluginInfo")

	task0 := got["task"].(map[string]any)

	// Both plugins wrapped every string, and they composed in declaration
	// order: each wraps what the ones before it left, so the last installed is
	// the outermost.
	require.Equal(t, "[<One>]", task0["title"])
	require.Equal(t, "[<One>]", task0["shout"])

	// A field that asked to be left alone, through per-field plugin data, was.
	require.Equal(t, "One", task0["quiet"])

	// Every field was shown to the plugin, including the ones derived from
	// struct fields and the root's.
	require.Contains(t, first.seen, "Task.title")
	require.Contains(t, first.seen, "Task.shout")
	require.Contains(t, first.seen, "Query.task")
	require.Greater(t, first.calls.Load(), int32(0))
}

// TestPluginCanRefuseASchema covers the last capability: a plugin that objects
// at build time, with a message naming itself.
func TestPluginCanRefuseASchema(t *testing.T) {
	b := lightning.New(&picky{forbidden: "title"})
	lightning.Object[Task](b)
	b.Query().Field("task", func(ctx context.Context, _ *lightning.Root) (*Task, error) {
		return nil, nil
	})

	_, err := b.Build()
	require.ErrorContains(t, err, "Task declares title, which this schema does not allow")
}

// TestPluginErrorsNameThePlugin checks that a failure from a plugin says which
// plugin objected, since a schema may have several.
func TestPluginErrorsNameThePlugin(t *testing.T) {
	b := lightning.New(&broken{})
	lightning.Object[Task](b)
	b.Query().Field("task", func(ctx context.Context, _ *lightning.Root) (*Task, error) {
		return nil, nil
	})

	_, err := b.Build()
	require.ErrorContains(t, err, "plugin broken")
	require.ErrorContains(t, err, "cannot do its job")
}

type broken struct{}

func (b *broken) PluginName() string                   { return "broken" }
func (b *broken) BeforeBuild(*lightning.Builder) error { return fmt.Errorf("cannot do its job") }
