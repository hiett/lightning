package graphql_test

import (
	"context"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/internal"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/kylelemons/godebug/pretty"
	"github.com/stretchr/testify/require"
)

type GatewayType int

const (
	GatewayTypeVehicle GatewayType = iota
	GatewayTypeAsset
)

// Gateway is a GraphQL union, which is to say a Go interface with nothing on
// it. There is no marker struct and no one-hot invariant to get wrong: a value
// either is a Vehicle or is an Asset, because that is what a Go interface says.
type Gateway interface{ isGateway() }

type Vehicle struct {
	lightning.Meta `graphql:"Vehicle"`

	Name  string
	Speed int64
}

type Asset struct {
	lightning.Meta `graphql:"Asset"`

	Name         string
	BatteryLevel int64
}

func (v *Vehicle) isGateway() {}
func (a *Asset) isGateway()   {}

type gatewayArgs struct{ Type GatewayType }

func gatewaySchema(t *testing.T) *graphql.Schema {
	t.Helper()

	b := lightning.New()

	lightning.Enum(b, "GatewayType", map[string]GatewayType{
		"vehicle": GatewayTypeVehicle,
		"asset":   GatewayTypeAsset,
	})

	gateway := lightning.Union[Gateway](b)
	vehicle := lightning.Object[Vehicle](b)
	asset := lightning.Object[Asset](b)
	lightning.Implements(gateway, vehicle, func(v *Vehicle) Gateway { return v })
	lightning.Implements(gateway, asset, func(a *Asset) Gateway { return a })

	b.Query().FieldArgs("gateway", func(ctx context.Context, _ *lightning.Root, args gatewayArgs) (Gateway, error) {
		if args.Type == GatewayTypeVehicle {
			return &Vehicle{Name: "a", Speed: 50}, nil
		}
		return &Asset{Name: "b", BatteryLevel: 5}, nil
	})

	return b.MustBuild()
}

func TestUnionType(t *testing.T) {
	builtSchema := gatewaySchema(t)
	ctx := context.Background()

	q := graphql.MustParse(`
		{
			asset: gateway(type: asset) { __typename ... on Asset { name batteryLevel } ... on Vehicle { name speed } }
			vehicle: gateway(type: vehicle) { __typename ... on Asset { name batteryLevel } ... on Vehicle { name speed } }
		}
	`, nil)

	require.NoError(t, graphql.PrepareQuery(ctx, builtSchema.Query, q.SelectionSet))

	e := testgraphql.NewExecutorWrapper(t)
	result, err := e.Execute(ctx, builtSchema.Query, nil, q)
	require.NoError(t, err)

	if d := pretty.Compare(internal.AsJSON(result), internal.ParseJSON(`
		{"vehicle": { "name": "a", "speed": "50", "__typename": "Vehicle" }, "asset": { "name": "b", "batteryLevel": "5", "__typename": "Asset" }}`)); d != "" {
		t.Errorf("expected did not match result: %s", d)
	}
}

// TestUnionPrints checks that a union prints as one, with its members.
func TestUnionPrints(t *testing.T) {
	sdl, err := graphql.PrintSchema(gatewaySchema(t))
	require.NoError(t, err)
	require.Contains(t, sdl, "union Gateway = Asset | Vehicle")
}

// TestUnionRejectsAnUnregisteredMember checks the one thing that can still go
// wrong: returning a Go type that satisfies the interface but was never
// registered as a member.
//
// Satisfying an interface by accident is ordinary Go; joining a union by
// accident is not, so membership is declared and a stranger is an error rather
// than a silently missing type.
func TestUnionRejectsAnUnregisteredMember(t *testing.T) {
	b := lightning.New()

	gateway := lightning.Union[Gateway](b)
	vehicle := lightning.Object[Vehicle](b)
	lightning.Implements(gateway, vehicle, func(v *Vehicle) Gateway { return v })

	b.Query().Field("gateway", func(ctx context.Context, _ *lightning.Root) (Gateway, error) {
		// An Asset satisfies Gateway but was never registered.
		return &Asset{Name: "b"}, nil
	})

	built := b.MustBuild()
	q := graphql.MustParse(`{ gateway { __typename } }`, nil)
	require.NoError(t, graphql.PrepareQuery(context.Background(), built.Query, q.SelectionSet))

	for _, execWithName := range testgraphql.GetExecutors() {
		t.Run(execWithName.Name, func(t *testing.T) {
			_, err := execWithName.Executor.Execute(context.Background(), built.Query, nil, q)
			require.ErrorContains(t, err, "lightning.Implements")
		})
	}
}

type UnionPart1 struct {
	lightning.Meta `graphql:"UnionPart1"`

	OtherThing string
}

type UnionPart2 struct {
	lightning.Meta `graphql:"UnionPart2"`

	Thing string
}

// Part is a union over two trivial types, for the list and nesting cases.
type Part interface{ isPart() }

func (p *UnionPart1) isPart() {}
func (p *UnionPart2) isPart() {}

type WrapperType struct {
	lightning.Meta `graphql:"WrapperType"`

	X Part
}

func partBuilder() (*lightning.Builder, *lightning.AbstractType[Part]) {
	b := lightning.New()
	part := lightning.Union[Part](b)
	one := lightning.Object[UnionPart1](b)
	two := lightning.Object[UnionPart2](b)
	lightning.Implements(part, one, func(p *UnionPart1) Part { return p })
	lightning.Implements(part, two, func(p *UnionPart2) Part { return p })
	return b, part
}

func TestUnionList(t *testing.T) {
	b, _ := partBuilder()
	b.Query().Field("list", func(ctx context.Context, _ *lightning.Root) ([]Part, error) {
		return []Part{
			&UnionPart2{Thing: "b"},
			&UnionPart1{OtherThing: "a"},
		}, nil
	})

	builtSchema := b.MustBuild()
	ctx := context.Background()

	q := graphql.MustParse(`{ list { ... on UnionPart1 { otherThing } ... on UnionPart2 { thing } } }`, nil)
	require.NoError(t, graphql.PrepareQuery(ctx, builtSchema.Query, q.SelectionSet))

	e := testgraphql.NewExecutorWrapper(t)
	result, err := e.Execute(ctx, builtSchema.Query, nil, q)
	require.NoError(t, err)

	if d := pretty.Compare(internal.AsJSON(result), internal.ParseJSON(`
		{ "list": [{"thing": "b"}, { "otherThing": "a" } ] }`)); d != "" {
		t.Errorf("expected did not match result: %s", d)
	}
}

func TestUnionStruct(t *testing.T) {
	b, _ := partBuilder()
	lightning.Object[WrapperType](b)
	b.Query().Field("wrapper", func(ctx context.Context, _ *lightning.Root) (*WrapperType, error) {
		return &WrapperType{X: &UnionPart2{Thing: "b"}}, nil
	})

	builtSchema := b.MustBuild()
	ctx := context.Background()

	q := graphql.MustParse(`{ wrapper { x {... on UnionPart1 { otherThing } ... on UnionPart2 { thing } } } }`, nil)
	require.NoError(t, graphql.PrepareQuery(ctx, builtSchema.Query, q.SelectionSet))

	e := testgraphql.NewExecutorWrapper(t)
	result, err := e.Execute(ctx, builtSchema.Query, nil, q)
	require.NoError(t, err)

	if d := pretty.Compare(internal.AsJSON(result), internal.ParseJSON(`
		{ "wrapper": { "x": { "thing": "b"} } }`)); d != "" {
		t.Errorf("expected did not match result: %s", d)
	}
}

// TestUnionOverANonInterfaceIsReported checks the mistake the Go type system
// cannot catch on its own.
func TestUnionOverANonInterfaceIsReported(t *testing.T) {
	b := lightning.New()
	lightning.Union[Vehicle](b)
	b.Query().Field("ok", func(ctx context.Context, _ *lightning.Root) (bool, error) { return true, nil })

	_, err := b.Build()
	require.ErrorContains(t, err, "is not a Go interface")
}
