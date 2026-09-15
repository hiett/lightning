package introspection_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql/introspection"
	"github.com/hiett/lightning/internal/snapshotter"
	"github.com/hiett/lightning/relay"
	"github.com/stretchr/testify/require"
)

type User struct {
	lightning.Meta `graphql:"user"`

	Key      string `graphql:"-"`
	Name     string
	MaybeAge *int64
	Uuid     Uuid
}

func (u *User) NodeID() string { return u.Name }

type Vehicle struct {
	lightning.Meta `graphql:"Vehicle"`

	Name  string
	Speed int64
	Uuid  Uuid
}

type Asset struct {
	lightning.Meta `graphql:"Asset"`

	Name         string
	BatteryLevel int64
	Uuid         Uuid
}

// Gateway is a union over the two.
type Gateway interface{ isGateway() }

func (v *Vehicle) isGateway() {}
func (a *Asset) isGateway()   {}

type enumType int32

// GreetArgs shows every argument shape: a required scalar, an optional input
// object, an enum, and a scalar with a default.
type GreetArgs struct {
	Other     string
	Include   *GreetTarget
	Enumfield enumType
	Optional  string `default:""`
}

type GreetTarget struct {
	lightning.Meta `graphql:"GreetTarget"`

	Name string
}

func makeSchema() *lightning.Builder {
	b := lightning.New(relay.Plugin())

	lightning.Enum(b, "enumType", map[string]enumType{
		"random":  enumType(3),
		"random1": enumType(2),
		"random2": enumType(1),
	})

	user := lightning.Object[User](b)
	relay.Node(b, func(ctx context.Context, id string) (*User, error) { return nil, nil })

	gateway := lightning.Union[Gateway](b)
	vehicle := lightning.Object[Vehicle](b)
	asset := lightning.Object[Asset](b)
	lightning.Implements(gateway, vehicle, func(v *Vehicle) Gateway { return v })
	lightning.Implements(gateway, asset, func(a *Asset) Gateway { return a })

	query := b.Query()
	query.Field("me", func(ctx context.Context, _ *lightning.Root) (User, error) {
		return User{Name: "me"}, nil
	})
	query.Field("noone", func(ctx context.Context, _ *lightning.Root) (*User, error) {
		return &User{Name: "me"}, nil
	}).NonNull()
	query.Field("nullableUser", func(ctx context.Context, _ *lightning.Root) (*User, error) {
		return nil, nil
	})
	// A list of values, which is how a list with non-null entries is spelled.
	query.Field("usersPtrForceNonNullable", func(ctx context.Context, _ *lightning.Root) ([]User, error) {
		return nil, nil
	})
	query.Field("usersPtr", func(ctx context.Context, _ *lightning.Root) ([]*User, error) {
		return nil, nil
	})
	relay.Connection(query, "usersConnection", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]User, error) {
		return nil, nil
	})
	relay.Connection(query, "usersConnectionPtr", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*User, error) {
		return nil, nil
	})
	query.Field("userUuid", func(ctx context.Context, _ *lightning.Root) (*Uuid, error) {
		return nil, nil
	})
	query.Field("usersUuid", func(ctx context.Context, _ *lightning.Root) ([]Uuid, error) {
		return nil, nil
	})
	query.Field("gateway", func(ctx context.Context, _ *lightning.Root) (Gateway, error) {
		return nil, nil
	})
	query.Field("viewer", func(ctx context.Context, _ *lightning.Root) (User, error) {
		return User{Name: "me"}, nil
	})

	user.Field("friends", func(ctx context.Context, u *User) ([]User, error) {
		return nil, nil
	})
	user.FieldArgs("greet", func(ctx context.Context, u *User, args GreetArgs) (string, error) {
		return "", nil
	})

	b.Mutation().Field("sayHi", func(ctx context.Context, _ *lightning.Root) (bool, error) {
		return true, nil
	})

	return b
}

func TestComputeSchemaJSON(t *testing.T) {
	snap := snapshotter.New(t)
	defer snap.Verify()

	actualBytes, err := introspection.ComputeSchemaJSON(makeSchema().MustBuild())
	require.NoError(t, err)

	var actual map[string]interface{}
	require.NoError(t, json.Unmarshal(actualBytes, &actual))
	snap.Snapshot("schema", actual)
}

// Uuid is a stub version of a "Text Marshalable" type.
type Uuid struct{}

func (u Uuid) MarshalText() ([]byte, error) {
	return nil, nil
}

func (u *Uuid) UnmarshalText(data []byte) error {
	return nil
}
