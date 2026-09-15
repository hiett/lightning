package schema

import (
	"context"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/relay"
)

// AddTaskArgs are the arguments to the addTask mutation.
//
// Each argument documents itself, next to where it is declared. A value field
// is required; a pointer is optional.
type AddTaskArgs struct {
	Title   string    `description:"What needs doing."`
	OwnerID relay.GID `graphql:"ownerId" description:"The global id of the user or team it belongs to."`
}

// SetTaskDoneArgs are the arguments to the setTaskDone mutation.
//
// The identifier is typed, so a client that sends a user's global id where a
// task's was asked for is told so while the query is prepared, and the resolver
// is handed the local identifier with no decoding of its own to do.
type SetTaskDoneArgs struct {
	ID   relay.ID[Task] `graphql:"id" description:"The task's global id."`
	Done bool           `description:"The new state."`
}

// Build assembles the example schema over a store.
func Build(store *Store) *graphql.Schema {
	b := lightning.New(relay.Plugin())

	lightning.Enum(b, "TaskStatus", map[string]Status{
		"TODO": StatusTodo,
		"DONE": StatusDone,
	}).Describe("How far along a task is.")

	// --- Actor: an interface with two implementations ------------------------
	//
	// The field is declared once, here, and every member inherits it.
	actor := lightning.Interface[Actor](b).Describe("Whoever a task belongs to: a person or a team.")
	actor.Field("displayName", func(_ context.Context, a Actor) (string, error) {
		return a.DisplayName(), nil
	}).Describe("The name to show for this actor.")

	// --- The types -----------------------------------------------------------
	//
	// Each struct already describes itself, so declaring it is one line. Making
	// it a node is one more.
	user := lightning.Object[User](b)
	team := lightning.Object[Team](b)
	task := lightning.Object[Task](b)

	relay.Node(b, store.User)
	relay.Node(b, store.Team)
	relay.Node(b, store.Task)

	lightning.Implements(actor, user, func(u *User) Actor { return u })
	lightning.Implements(actor, team, func(t *Team) Actor { return t })

	task.Field("owner", store.Owner).Describe("Whoever the task belongs to.")
	task.Attr("status", store.Status).Describe("How far along the task is.")

	// --- Query ---------------------------------------------------------------
	q := b.Query()
	q.Field("viewer", store.Viewer).Describe("The signed-in user. This example always signs in as Ada.")

	relay.Connection(q, "tasks", func(ctx context.Context, _ *lightning.Root, _ relay.Page) ([]*Task, error) {
		return store.Tasks(ctx)
	}).Describe("Every task, oldest first.")

	// --- Mutation ------------------------------------------------------------
	m := b.Mutation()
	m.FieldArgs("addTask", func(ctx context.Context, _ *lightning.Root, args AddTaskArgs) (*Task, error) {
		return store.AddTask(ctx, args.Title, args.OwnerID.Local)
	}).Describe("Adds a task.")

	m.FieldArgs("setTaskDone", func(ctx context.Context, _ *lightning.Root, args SetTaskDoneArgs) (*Task, error) {
		return store.SetTaskDone(ctx, args.ID.Local, args.Done)
	}).Describe("Marks a task done, or not done.")

	// --- Subscription --------------------------------------------------------
	//
	// A subscription field is an ordinary field. What makes it live is the
	// dependency the store records while resolving it: when the store
	// invalidates that key, this operation re-executes and the new result is
	// pushed to every subscribed client.
	relay.Connection(b.Subscription(), "tasks", func(ctx context.Context, _ *lightning.Root, _ relay.Page) ([]*Task, error) {
		return store.Tasks(ctx)
	}).Describe("Every task, pushed again whenever any of them changes.")

	return b.MustBuild()
}

// MustBuild builds the example schema over a fresh store, for the schema
// exporter, which needs the shape rather than the data.
func MustBuild() *graphql.Schema {
	return Build(NewStore(nil))
}
