// Package schema is the example application's data store and GraphQL schema.
//
// It exists to exercise, end to end, the pieces a Relay client needs from
// lightning: an interface, the Node interface and global identifiers, a Relay
// connection, a mutation, and a live query that updates when the data changes.
package schema

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/invalidation"
)

// Task is a unit of work.
//
// Everything the schema knows about this type is written here: its GraphQL
// name, its description, which fields are exposed, and what each one means.
type Task struct {
	lightning.Meta `graphql:"Task" description:"A unit of work."`

	Key     string    `graphql:"-"`
	Title   string    `description:"What needs doing." sortable:"true" filterable:"true"`
	Done    bool      `description:"Whether it has been done." sortable:"true"`
	OwnerID string    `graphql:"-"`
	Added   time.Time `description:"When it was added." sortable:"true"`
}

// NodeID gives the task its type-local identifier, which is all relay.Node
// needs beyond a way to fetch one.
func (t *Task) NodeID() string { return t.Key }

// User is a person.
type User struct {
	lightning.Meta `graphql:"User" description:"A person."`

	Key   string `graphql:"-"`
	Name  string `description:"The user's display name."`
	Email string `description:"Where to reach them."`
}

func (u *User) NodeID() string      { return u.Key }
func (u *User) DisplayName() string { return u.Name }

// Team is a group of people. It exists so that Actor has more than one possible
// type, which is what makes the interface worth having.
type Team struct {
	lightning.Meta `graphql:"Team" description:"A group of people."`

	Key     string `graphql:"-"`
	Name    string `description:"The team's display name."`
	Members int32  `description:"How many people are on it."`
}

func (t *Team) NodeID() string      { return t.Key }
func (t *Team) DisplayName() string { return t.Name }

// Actor is whoever a task belongs to: an ordinary Go interface, which is what a
// GraphQL interface is backed by. A resolver returns a *User or a *Team and the
// schema works out which type that is.
type Actor interface {
	DisplayName() string
}

// Status is how far along a task is.
type Status int32

const (
	StatusTodo Status = iota
	StatusDone
)

// Invalidation keys. Their granularity is a design choice: a live query that
// read one task re-runs when that task changes, and one that read the list
// re-runs when the list changes.
const taskListKey = "tasks"

func taskKey(id string) string { return "task:" + id }
func userKey(id string) string { return "user:" + id }
func teamKey(id string) string { return "team:" + id }

// Store is an in-memory database. Every read records an invalidation key and
// every write announces one, which is the whole contract a live query needs.
type Store struct {
	invalidator *invalidation.Invalidator

	mu    sync.RWMutex
	tasks map[string]*Task
	users map[string]*User
	teams map[string]*Team
	next  int
}

// NewStore returns a store seeded with a little data.
func NewStore(invalidator *invalidation.Invalidator) *Store {
	s := &Store{
		invalidator: invalidator,
		tasks:       map[string]*Task{},
		users:       map[string]*User{},
		teams:       map[string]*Team{},
	}

	s.users["u1"] = &User{Key: "u1", Name: "Ada", Email: "ada@example.com"}
	s.users["u2"] = &User{Key: "u2", Name: "Grace", Email: "grace@example.com"}
	s.teams["t1"] = &Team{Key: "t1", Name: "Platform", Members: 4}

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, seed := range []struct {
		title string
		owner string
		done  bool
	}{
		{"Write the schema", "u1", true},
		{"Export schema.graphql", "u1", true},
		{"Wire up relay-compiler", "u2", false},
		{"Paginate the task list", "t1", false},
		{"Make it live", "u2", false},
	} {
		id := s.newID()
		s.tasks[id] = &Task{
			Key:     id,
			Title:   seed.title,
			OwnerID: seed.owner,
			Done:    seed.done,
			Added:   start.Add(time.Duration(i) * time.Hour),
		}
	}

	return s
}

func (s *Store) newID() string {
	s.next++
	return fmt.Sprintf("task%d", s.next)
}

// depend records an invalidation key, if there is an invalidator to record it
// with. The schema exporter builds a store without one.
func (s *Store) depend(ctx context.Context, keys ...string) {
	if s.invalidator != nil {
		s.invalidator.Depend(ctx, keys...)
	}
}

func (s *Store) invalidate(ctx context.Context, keys ...string) error {
	if s.invalidator == nil {
		return nil
	}
	return s.invalidator.Invalidate(ctx, keys...)
}

// Tasks returns every task, oldest first.
func (s *Store) Tasks(ctx context.Context) ([]*Task, error) {
	s.depend(ctx, taskListKey)

	s.mu.RLock()
	defer s.mu.RUnlock()

	tasks := make([]*Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		// A copy, so a later write cannot mutate a value a live query is still
		// holding.
		copied := *task
		tasks = append(tasks, &copied)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Added.Equal(tasks[j].Added) {
			return tasks[i].Key < tasks[j].Key
		}
		return tasks[i].Added.Before(tasks[j].Added)
	})
	return tasks, nil
}

// Task returns one task, or nil if there is no such task.
func (s *Store) Task(ctx context.Context, id string) (*Task, error) {
	s.depend(ctx, taskKey(id))

	s.mu.RLock()
	defer s.mu.RUnlock()

	task, ok := s.tasks[id]
	if !ok {
		return nil, nil
	}
	copied := *task
	return &copied, nil
}

// User returns one user, or nil.
func (s *Store) User(ctx context.Context, id string) (*User, error) {
	s.depend(ctx, userKey(id))

	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[id]
	if !ok {
		return nil, nil
	}
	copied := *user
	return &copied, nil
}

// Team returns one team, or nil.
func (s *Store) Team(ctx context.Context, id string) (*Team, error) {
	s.depend(ctx, teamKey(id))

	s.mu.RLock()
	defer s.mu.RUnlock()

	team, ok := s.teams[id]
	if !ok {
		return nil, nil
	}
	copied := *team
	return &copied, nil
}

// Viewer returns the signed-in user. This example always signs in as Ada.
func (s *Store) Viewer(ctx context.Context, _ *lightning.Root) (*User, error) {
	return s.User(ctx, "u1")
}

// Owner returns whoever a task belongs to, as an Actor.
//
// The resolver returns the interface value itself: there is no wrapper struct
// to build, and returning something that is not an Actor would not compile.
func (s *Store) Owner(ctx context.Context, task *Task) (Actor, error) {
	if user, err := s.User(ctx, task.OwnerID); err != nil {
		return nil, err
	} else if user != nil {
		return user, nil
	}
	team, err := s.Team(ctx, task.OwnerID)
	if err != nil || team == nil {
		return nil, err
	}
	return team, nil
}

// Status reports how far along a task is.
func (s *Store) Status(task *Task) Status {
	if task.Done {
		return StatusDone
	}
	return StatusTodo
}

// AddTask creates a task and announces that the list changed.
func (s *Store) AddTask(ctx context.Context, title, ownerID string) (*Task, error) {
	s.mu.Lock()
	if _, ok := s.users[ownerID]; !ok {
		if _, ok := s.teams[ownerID]; !ok {
			s.mu.Unlock()
			return nil, fmt.Errorf("no owner %q", ownerID)
		}
	}

	id := s.newID()
	task := &Task{Key: id, Title: title, OwnerID: ownerID, Added: time.Now().UTC()}
	s.tasks[id] = task
	copied := *task
	s.mu.Unlock()

	if err := s.invalidate(ctx, taskListKey); err != nil {
		return nil, err
	}
	return &copied, nil
}

// SetTaskDone changes a task and announces that it, and the list, changed.
func (s *Store) SetTaskDone(ctx context.Context, id string, done bool) (*Task, error) {
	s.mu.Lock()
	task, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("no task %q", id)
	}
	task.Done = done
	copied := *task
	s.mu.Unlock()

	// Both keys: a live query watching the list sees the change, and so does
	// one that fetched this single task by its global id.
	if err := s.invalidate(ctx, taskListKey, taskKey(id)); err != nil {
		return nil, err
	}
	return &copied, nil
}
