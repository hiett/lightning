package lightning_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/stretchr/testify/require"
)

// Actor is a GraphQL interface, which is to say an ordinary Go interface.
// A resolver returns one of these; the concrete Go type decides __typename.
type Actor interface {
	DisplayName() string
}

type Person struct {
	lightning.Meta `description:"A person."`

	Name  string `description:"Their name."`
	Email string `description:"Where to reach them."`
}

type Squad struct {
	lightning.Meta `description:"A group of people."`

	Name    string `description:"The squad's name."`
	Members int32  `description:"How many people are in it."`
}

func (p *Person) DisplayName() string { return p.Name }
func (s *Squad) DisplayName() string  { return s.Name }

// actorSchema declares the interface and its two members.
func actorSchema(t *testing.T) *graphql.Schema {
	t.Helper()

	b := lightning.New()

	actor := lightning.Interface[Actor](b)
	actor.Describe("Whoever a task belongs to.")
	actor.Field("displayName", func(ctx context.Context, a Actor) (string, error) {
		return a.DisplayName(), nil
	}).Describe("The name to show.")

	person := lightning.Object[Person](b)
	squad := lightning.Object[Squad](b)

	// The witness function is the membership check: it only compiles because
	// *Person and *Squad satisfy Actor.
	lightning.Implements(actor, person, func(p *Person) Actor { return p })
	lightning.Implements(actor, squad, func(s *Squad) Actor { return s })

	b.Query().Field("owner", func(ctx context.Context, _ *lightning.Root) (Actor, error) {
		return &Person{Name: "Ada", Email: "ada@example.com"}, nil
	})
	b.Query().Field("owners", func(ctx context.Context, _ *lightning.Root) ([]Actor, error) {
		return []Actor{
			&Person{Name: "Ada", Email: "ada@example.com"},
			&Squad{Name: "Platform", Members: 4},
		}, nil
	})

	return b.MustBuild()
}

// TestInterfaceIsAGoInterface covers the headline: no marker struct, no one-hot
// wrapper, the resolver returns the interface value itself.
func TestInterfaceIsAGoInterface(t *testing.T) {
	schema := actorSchema(t)

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	require.Contains(t, sdl, "interface Actor {")
	require.Contains(t, sdl, "type Person implements Actor {")
	require.Contains(t, sdl, "type Squad implements Actor {")

	got := run(t, schema, `{ owner { __typename displayName ... on Person { email } } }`)
	require.Equal(t, map[string]any{"owner": map[string]any{
		"__typename":  "Person",
		"displayName": "Ada",
		"email":       "ada@example.com",
	}}, got)
}

// TestInterfaceFragments covers both fragment forms over a list.
func TestInterfaceFragments(t *testing.T) {
	schema := actorSchema(t)

	got := run(t, schema, `
	{
		owners {
			__typename
			displayName
			...PersonBits
			... on Squad { members }
		}
	}
	fragment PersonBits on Person { email }`)

	owners := got["owners"].([]any)
	require.Len(t, owners, 2)

	first := owners[0].(map[string]any)
	require.Equal(t, "Person", first["__typename"])
	require.Equal(t, "ada@example.com", first["email"])
	require.NotContains(t, first, "members")

	second := owners[1].(map[string]any)
	require.Equal(t, "Squad", second["__typename"])
	require.Equal(t, float64(4), second["members"])
	require.NotContains(t, second, "email")
}

// TestInterfaceMembershipIsExplicit checks that a Go type which happens to
// satisfy the interface is not enlisted without being registered.
func TestInterfaceMembershipIsExplicit(t *testing.T) {
	b := lightning.New()

	actor := lightning.Interface[Actor](b)
	actor.Field("displayName", func(ctx context.Context, a Actor) (string, error) {
		return a.DisplayName(), nil
	})

	person := lightning.Object[Person](b)
	lightning.Implements(actor, person, func(p *Person) Actor { return p })

	// Squad satisfies Actor in Go, but was never registered as a member.
	lightning.Object[Squad](b)

	b.Query().Field("owner", func(ctx context.Context, _ *lightning.Root) (Actor, error) {
		return &Squad{Name: "Platform"}, nil
	})
	b.Query().Field("squad", func(ctx context.Context, _ *lightning.Root) (*Squad, error) {
		return nil, nil
	})

	schema := b.MustBuild()

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	require.NotContains(t, sdl, "type Squad implements Actor",
		"satisfying a Go interface by accident must not join a GraphQL interface")

	// Returning an unregistered type through the interface is an error rather
	// than a wrong __typename.
	q := graphql.MustParse(`{ owner { displayName } }`, nil)
	require.NoError(t, graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet))
	e := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
	_, err = e.Execute(context.Background(), schema.Query, nil, q)
	require.Error(t, err)
	require.Contains(t, err.Error(), "lightning.Implements")
}

// TestInterfaceFieldIsInherited checks that a field declared on the interface
// appears on every member without being declared again.
func TestInterfaceFieldIsInherited(t *testing.T) {
	sdl, err := graphql.PrintSchema(actorSchema(t))
	require.NoError(t, err)

	// Neither Person nor Squad declares displayName; both have it.
	require.Contains(t, sdl, "type Person implements Actor {")
	require.Contains(t, sdl, "type Squad implements Actor {")
	require.Equal(t, 3, strings.Count(sdl, "displayName"),
		"once on the interface and once on each member; printed schema:\n%s", sdl)
}

// TestInterfaceContractIsChecked covers the build-time check that a member
// declaring a field of the interface's name gives it a matching type.
func TestInterfaceContractIsChecked(t *testing.T) {
	type Badge struct {
		lightning.Meta `description:"Has a displayName of the wrong type."`

		// The interface declares displayName as String!; here it is an Int.
		DisplayName int32
	}

	b := lightning.New()

	actor := lightning.Interface[Actor](b)
	actor.Field("displayName", func(ctx context.Context, a Actor) (string, error) {
		return a.DisplayName(), nil
	})

	badge := lightning.Object[Badge](b)
	lightning.Implements(actor, badge, func(x *Badge) Actor { return nil })

	b.Query().Field("owner", func(ctx context.Context, _ *lightning.Root) (Actor, error) { return nil, nil })

	_, err := b.Build()
	require.Error(t, err)
	require.Contains(t, err.Error(), "Badge.displayName is Int!")
	require.Contains(t, err.Error(), "the interface Actor declares it as String!")
}

// Shape is a union: a Go interface with no methods of its own.
type Shape interface{ isShape() }

type Circle struct {
	lightning.Meta `description:"A circle."`
	Radius         float64
}
type Rect struct {
	lightning.Meta `description:"A rectangle."`
	W, H           float64
}

func (*Circle) isShape() {}
func (*Rect) isShape()   {}

// TestUnion covers the union form.
func TestUnion(t *testing.T) {
	b := lightning.New()

	shape := lightning.Union[Shape](b)
	shape.Describe("Something with an area.")

	circle := lightning.Object[Circle](b)
	rect := lightning.Object[Rect](b)
	lightning.Implements(shape, circle, func(c *Circle) Shape { return c })
	lightning.Implements(shape, rect, func(r *Rect) Shape { return r })

	b.Query().Field("shape", func(ctx context.Context, _ *lightning.Root) (Shape, error) {
		return &Circle{Radius: 2}, nil
	})

	schema := b.MustBuild()

	sdl, err := graphql.PrintSchema(schema)
	require.NoError(t, err)
	require.Contains(t, sdl, "union Shape = Circle | Rect")

	got := run(t, schema, `{ shape { __typename ... on Circle { radius } } }`)
	require.Equal(t, map[string]any{"shape": map[string]any{
		"__typename": "Circle",
		"radius":     float64(2),
	}}, got)
}
