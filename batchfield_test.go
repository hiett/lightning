package lightning_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/hiett/lightning"
	"github.com/stretchr/testify/require"
)

// Owner is the type a batched field resolves to.
type Owner struct {
	lightning.Meta `description:"Someone a task belongs to."`

	Name string `description:"Their name."`
}

// listSchema builds a schema whose query returns several tasks, so that a field
// on Task is asked for more than one parent at a time.
func listSchema(t *testing.T, declare func(task *lightning.Type[Task])) *lightning.Builder {
	t.Helper()

	b := lightning.New()
	lightning.Object[Owner](b)
	task := lightning.Object[Task](b)
	declare(task)

	b.Query().Field("tasks", func(ctx context.Context, _ *lightning.Root) ([]*Task, error) {
		return []*Task{
			{Key: "t1", Title: "One"},
			{Key: "t2", Title: "Two"},
			{Key: "t3", Title: "Three"},
		}, nil
	})

	return b
}

// TestBatchResolvesEveryParentAtOnce is the point of batching: one call, not
// one per parent.
func TestBatchResolvesEveryParentAtOnce(t *testing.T) {
	var calls atomic.Int32

	b := listSchema(t, func(task *lightning.Type[Task]) {
		task.Batch("owner", func(ctx context.Context, tasks []*Task) ([]*Owner, error) {
			calls.Add(1)
			out := make([]*Owner, len(tasks))
			for i, task := range tasks {
				out[i] = &Owner{Name: "owner of " + task.Key}
			}
			return out, nil
		}).Describe("Who it belongs to.")
	})

	schema := b.MustBuild()

	got := run(t, schema, `{ tasks { key: title owner { name } } }`)
	tasks := got["tasks"].([]any)
	require.Len(t, tasks, 3)
	for i, want := range []string{"owner of t1", "owner of t2", "owner of t3"} {
		owner := tasks[i].(map[string]any)["owner"].(map[string]any)
		require.Equal(t, want, owner["name"])
	}

	require.Equal(t, int32(1), calls.Load(), "three parents should have cost one call")
}

// TestBatchTakesItsTypeFromTheResolver holds the central convention: a batch
// field is an ordinary field, so the element type still says everything.
func TestBatchTakesItsTypeFromTheResolver(t *testing.T) {
	b := listSchema(t, func(task *lightning.Type[Task]) {
		task.Batch("nullableOwner", func(ctx context.Context, tasks []*Task) ([]*Owner, error) {
			return make([]*Owner, len(tasks)), nil
		})
		task.Batch("wordCount", func(ctx context.Context, tasks []*Task) ([]int32, error) {
			return make([]int32, len(tasks)), nil
		})
	})

	sdl := printSchema(t, b.MustBuild())
	require.Contains(t, sdl, "nullableOwner: Owner\n")
	require.Contains(t, sdl, "wordCount: Int!\n")
}

// TestBatchFillsNullsForMissingResults covers the ordinary case of a lookup
// that finds nothing: a nil result is null, and the parents around it are
// unaffected.
func TestBatchFillsNullsForMissingResults(t *testing.T) {
	b := listSchema(t, func(task *lightning.Type[Task]) {
		task.Batch("owner", func(ctx context.Context, tasks []*Task) ([]*Owner, error) {
			out := make([]*Owner, len(tasks))
			for i, task := range tasks {
				if task.Key == "t2" {
					continue
				}
				out[i] = &Owner{Name: task.Key}
			}
			return out, nil
		})
	})

	got := run(t, b.MustBuild(), `{ tasks { owner { name } } }`)
	tasks := got["tasks"].([]any)
	require.Equal(t, "t1", tasks[0].(map[string]any)["owner"].(map[string]any)["name"])
	require.Nil(t, tasks[1].(map[string]any)["owner"])
	require.Equal(t, "t3", tasks[2].(map[string]any)["owner"].(map[string]any)["name"])
}

// TestLoadDeduplicatesKeys is the reason Load exists: three tasks that share an
// owner cost one lookup, not three.
func TestLoadDeduplicatesKeys(t *testing.T) {
	var asked [][]string

	b := listSchema(t, func(task *lightning.Type[Task]) {
		task.Load("owner",
			func(t *Task) string {
				if t.Key == "t3" {
					return "someone-else"
				}
				return "shared"
			},
			func(ctx context.Context, keys []string) ([]*Owner, error) {
				asked = append(asked, keys)
				out := make([]*Owner, len(keys))
				for i, key := range keys {
					out[i] = &Owner{Name: key}
				}
				return out, nil
			})
	})

	got := run(t, b.MustBuild(), `{ tasks { owner { name } } }`)
	tasks := got["tasks"].([]any)

	require.Equal(t, [][]string{{"shared", "someone-else"}}, asked)
	require.Equal(t, "shared", tasks[0].(map[string]any)["owner"].(map[string]any)["name"])
	require.Equal(t, "shared", tasks[1].(map[string]any)["owner"].(map[string]any)["name"])
	require.Equal(t, "someone-else", tasks[2].(map[string]any)["owner"].(map[string]any)["name"])
}

// TestBatchArgsPassesTheSelectionsArguments covers arguments on a batch field.
// They come from one selection, so they are the same for every parent.
func TestBatchArgsPassesTheSelectionsArguments(t *testing.T) {
	type PrefixArgs struct {
		Prefix string `description:"What to put in front."`
	}

	b := listSchema(t, func(task *lightning.Type[Task]) {
		task.BatchArgs("label", func(ctx context.Context, tasks []*Task, args PrefixArgs) ([]string, error) {
			out := make([]string, len(tasks))
			for i, task := range tasks {
				out[i] = args.Prefix + task.Key
			}
			return out, nil
		})
	})

	got := run(t, b.MustBuild(), `{ tasks { label(prefix: "#") } }`)
	tasks := got["tasks"].([]any)
	require.Equal(t, "#t1", tasks[0].(map[string]any)["label"])
	require.Equal(t, "#t3", tasks[2].(map[string]any)["label"])
}

// TestUseBatchFallsBackToOneParentAtATime is the rollout switch. The same
// resolver answers either way, so there is no second implementation to keep in
// step with the first.
func TestUseBatchFallsBackToOneParentAtATime(t *testing.T) {
	var sizes []int

	b := listSchema(t, func(task *lightning.Type[Task]) {
		task.Batch("owner", func(ctx context.Context, tasks []*Task) ([]*Owner, error) {
			sizes = append(sizes, len(tasks))
			out := make([]*Owner, len(tasks))
			for i, task := range tasks {
				out[i] = &Owner{Name: task.Key}
			}
			return out, nil
		}).UseBatch(func(ctx context.Context) bool { return false })
	})

	got := run(t, b.MustBuild(), `{ tasks { owner { name } } }`)
	tasks := got["tasks"].([]any)
	require.Equal(t, "t1", tasks[0].(map[string]any)["owner"].(map[string]any)["name"])
	require.Equal(t, []int{1, 1, 1}, sizes)
}

// TestBatchReportsAMisalignedResult catches the one mistake the shape allows.
func TestBatchReportsAMisalignedResult(t *testing.T) {
	b := listSchema(t, func(task *lightning.Type[Task]) {
		task.Batch("owner", func(ctx context.Context, tasks []*Task) ([]*Owner, error) {
			return []*Owner{{Name: "only one"}}, nil
		})
	})

	_, err := runErr(t, b.MustBuild(), `{ tasks { owner { name } } }`)
	require.ErrorContains(t, err, "Task.owner: the batch resolver was given 3 parents and returned 1 results")
}

// TestLoadReportsAMisalignedResult does the same for the loader's contract.
func TestLoadReportsAMisalignedResult(t *testing.T) {
	b := listSchema(t, func(task *lightning.Type[Task]) {
		task.Load("owner",
			func(t *Task) string { return t.Key },
			func(ctx context.Context, keys []string) ([]*Owner, error) { return nil, nil })
	})

	_, err := runErr(t, b.MustBuild(), `{ tasks { owner { name } } }`)
	require.ErrorContains(t, err, "owner: asked for 3 keys and got 0 results")
}

// TestUseBatchOnAPlainFieldIsReported keeps the mistake at build time rather
// than letting it silently do nothing.
func TestUseBatchOnAPlainFieldIsReported(t *testing.T) {
	b := lightning.New()
	task := lightning.Object[Task](b)
	task.Attr("shout", func(t *Task) string { return t.Title }).
		UseBatch(func(ctx context.Context) bool { return true })

	b.Query().Field("task", func(ctx context.Context, _ *lightning.Root) (*Task, error) { return nil, nil })

	_, err := b.Build()
	require.ErrorContains(t, err, "UseBatch is for a field declared with Batch, BatchArgs or Load")
}

// TestBatchErrorsReachTheCaller checks that a failed lookup is reported rather
// than swallowed.
func TestBatchErrorsReachTheCaller(t *testing.T) {
	b := listSchema(t, func(task *lightning.Type[Task]) {
		task.Batch("owner", func(ctx context.Context, tasks []*Task) ([]*Owner, error) {
			return nil, fmt.Errorf("the owner store is down")
		})
	})

	_, err := runErr(t, b.MustBuild(), `{ tasks { owner { name } } }`)
	require.ErrorContains(t, err, "the owner store is down")
}
