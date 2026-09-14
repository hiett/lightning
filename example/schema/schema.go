package schema

import (
	"context"

	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/schemabuilder"
)

// Actor is a GraphQL interface implemented by User and Team: whoever a task
// belongs to.
//
// An interface is declared by a marker struct whose embedded pointers name its
// implementing types. A field returning an interface returns this struct with
// exactly one member set.
type Actor struct {
	schemabuilder.Interface

	*User
	*Team
}

// Build assembles the example schema over a store.
func Build(store *Store) *graphql.Schema {
	builder := schemabuilder.NewSchema()

	registerActor(builder)
	registerUser(builder, store)
	registerTeam(builder, store)
	registerTask(builder, store)
	registerQuery(builder, store)
	registerMutation(builder, store)
	registerSubscription(builder, store)

	return builder.MustBuild()
}

// MustBuild builds the example schema over a fresh store, for the schema
// exporter, which needs the shape rather than the data.
func MustBuild() *graphql.Schema {
	return Build(NewStore(nil))
}

func registerActor(builder *schemabuilder.Schema) {
	// displayName is declared explicitly rather than left to the default of
	// "every field the members share", because it is the interface's contract:
	// adding a field to both User and Team should not silently widen it.
	builder.Interface("Actor", Actor{}).
		Fields("id", "displayName").
		Describe("Whoever a task belongs to: a person or a team.")
}

func registerUser(builder *schemabuilder.Schema, store *Store) {
	user := builder.Object("User", User{})
	user.Describe("A person.")

	// Declaring the type a node gives it a global `id` and makes it reachable
	// through the root node field, which is what Relay's store needs to
	// normalise and refetch it.
	user.Node(
		func(u *User) string { return u.Key },
		func(ctx context.Context, id string) (*User, error) { return store.User(ctx, id), nil },
	)

	user.FieldFunc("displayName", func(u *User) string { return u.Name },
		schemabuilder.Description("The name to show for this actor."))
}

func registerTeam(builder *schemabuilder.Schema, store *Store) {
	team := builder.Object("Team", Team{})
	team.Describe("A group of people.")

	team.Node(
		func(t *Team) string { return t.Key },
		func(ctx context.Context, id string) (*Team, error) { return store.Team(ctx, id), nil },
	)

	team.FieldFunc("displayName", func(t *Team) string { return t.Name },
		schemabuilder.Description("The name to show for this actor."))
}

func registerTask(builder *schemabuilder.Schema, store *Store) {
	task := builder.Object("Task", Task{})
	task.Describe("A unit of work.")

	task.Node(
		func(t *Task) string { return t.Key },
		func(ctx context.Context, id string) (*Task, error) { return store.Task(ctx, id), nil },
	)
	// Task is paginated, so it needs a key field: the connection builds cursors
	// from it, and the live-query diff uses it to line up list elements.
	task.Key("key")

	task.FieldFunc("owner", func(ctx context.Context, t *Task) *Actor {
		if user := store.User(ctx, t.OwnerID); user != nil {
			return &Actor{User: user}
		}
		if team := store.Team(ctx, t.OwnerID); team != nil {
			return &Actor{Team: team}
		}
		return nil
	}, schemabuilder.Description("Whoever the task belongs to."))
}

func registerQuery(builder *schemabuilder.Schema, store *Store) {
	query := builder.Query()

	query.FieldFunc("viewer", func(ctx context.Context) *User {
		return store.User(ctx, "u1")
	}, schemabuilder.Description("The signed-in user. This example always signs in as Ada."))

	// Paginated turns a slice-returning field into a Relay connection:
	// TaskConnection, TaskEdge, cursors, pageInfo and the first/last/
	// before/after arguments.
	query.FieldFunc("tasks", func(ctx context.Context) []*Task {
		return store.Tasks(ctx)
	}, schemabuilder.Paginated, schemabuilder.Description("Every task, oldest first."))
}

func registerMutation(builder *schemabuilder.Schema, store *Store) {
	mutation := builder.Mutation()

	mutation.FieldFunc("addTask", func(ctx context.Context, args struct {
		Title   string
		OwnerId schemabuilder.ID
	}) (*Task, error) {
		owner, err := localID(args.OwnerId)
		if err != nil {
			return nil, err
		}
		return store.AddTask(ctx, args.Title, owner)
	}, schemabuilder.Description("Adds a task."),
		schemabuilder.ArgDescription("title", "What needs doing."),
		schemabuilder.ArgDescription("ownerId", "The global id of the user or team it belongs to."))

	mutation.FieldFunc("setTaskDone", func(ctx context.Context, args struct {
		Id   schemabuilder.ID
		Done bool
	}) (*Task, error) {
		id, err := localID(args.Id)
		if err != nil {
			return nil, err
		}
		return store.SetTaskDone(ctx, id, args.Done)
	}, schemabuilder.Description("Marks a task done, or not done."),
		schemabuilder.ArgDescription("id", "The task's global id."),
		schemabuilder.ArgDescription("done", "The new state."))
}

func registerSubscription(builder *schemabuilder.Schema, store *Store) {
	subscription := builder.Subscription()

	// A subscription field is an ordinary field. What makes it live is the
	// dependency the store records while resolving it: when the store
	// invalidates that key, this operation re-executes and the new result is
	// pushed to every subscribed client.
	subscription.FieldFunc("tasks", func(ctx context.Context) []*Task {
		return store.Tasks(ctx)
	}, schemabuilder.Paginated, schemabuilder.Description("Every task, pushed again whenever any of them changes."))
}

// localID recovers the type-local identifier from a global one.
//
// A mutation takes global ids because that is what a Relay client has to hand;
// the store speaks local ones.
//
// Decoding is strict. Falling back to treating an undecodable value as a local
// id would be friendlier in GraphiQL, but a type-local id can itself be valid
// base64 — "task1006" decodes to bytes containing a colon — so the fallback
// would mis-read exactly the ids it was meant to help with. Get an id from a
// query instead.
func localID(id schemabuilder.ID) (string, error) {
	_, local, err := (schemabuilder.Base64GlobalIDCodec{}).Decode(id.Value)
	if err != nil {
		return "", graphql.NewClientError("%q is not a global id; use the id a query returned", id.Value)
	}
	return local, nil
}
