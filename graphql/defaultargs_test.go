package graphql_test

import (
	"context"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/internal/testgraphql"
)

// defaultInner is what the query returns.
type defaultInner struct {
	lightning.Meta `graphql:"Inner"`

	OptionalValue string
	RequiredValue string
}

// defaultArgs has one argument of each kind: a value field is required, and a
// field with a default is not.
type defaultArgs struct {
	OptionalInput string `default:""`
	RequiredInput string
}

func TestDefaultArgs(t *testing.T) {
	b := lightning.New()
	lightning.Object[defaultInner](b)

	b.Query().FieldArgs("inner", func(ctx context.Context, _ *lightning.Root, input defaultArgs) (defaultInner, error) {
		return defaultInner{
			OptionalValue: input.OptionalInput,
			RequiredValue: input.RequiredInput,
		}, nil
	})

	builtSchema := b.MustBuild()

	snap := testgraphql.NewSnapshotter(t, builtSchema)
	defer snap.Verify()

	snap.SnapshotQuery("happy path all provided", `{
		inner(
			optionalInput: "teeeeeeeest", 
			requiredInput: "requiredInput!", 
		) { 
			optionalValue
			requiredValue
		}
	}`)

	snap.SnapshotQuery("missing required parameter", `{
		inner(
			optionalInput: "teeeeeeeest", 
		) { 
			optionalValue
			requiredValue
		}
	}`, testgraphql.RecordError)

	snap.SnapshotQuery("missing optional parameter does not error", `{
		inner(
			requiredInput: "teeeeeeeest", 
		) { 
			optionalValue
			requiredValue
		}
	}`)
}
