package graphql_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/concurrencylimiter"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/internal"
	"github.com/hiett/lightning/internal/testgraphql"
	"github.com/hiett/lightning/reactive"
	"github.com/stretchr/testify/assert"
)

// User is a person with a slow field hanging off it, for the caching and
// parallelism tests.
type User struct {
	lightning.Meta `graphql:"User"`

	Name     string
	Age      int64
	resource *reactive.Resource
}

// Slow is what User.slow resolves to, so that the test can count how often its
// own field is recomputed.
type Slow struct {
	lightning.Meta `graphql:"Slow"`
}

// pathInner and pathExpensive nest deeply enough for an error to have a path
// worth printing.
type pathInner struct {
	lightning.Meta `graphql:"inner"`
}

type pathExpensive struct {
	lightning.Meta `graphql:"expensive"`
}

func TestPathError(t *testing.T) {
	b := lightning.New()

	inner := lightning.Object[pathInner](b)
	inner.Field("expensive", func(ctx context.Context, _ *pathInner) (pathExpensive, error) {
		return pathExpensive{}, nil
	}).Expensive()
	inner.Field("inners", func(ctx context.Context, _ *pathInner) ([]pathInner, error) {
		return []pathInner{{}}, nil
	})

	nested := lightning.Object[pathExpensive](b)
	nested.Field("expensives", func(ctx context.Context, _ *pathExpensive) ([]pathExpensive, error) {
		return []pathExpensive{{}}, nil
	})
	nested.Field("err", func(ctx context.Context, _ *pathExpensive) (bool, error) {
		return false, errors.New("no good, bad")
	})

	query := b.Query()
	query.Field("inner", func(ctx context.Context, _ *lightning.Root) (pathInner, error) {
		return pathInner{}, nil
	})
	query.Field("safe", func(ctx context.Context, _ *lightning.Root) (bool, error) {
		return false, graphql.NewSafeError("safe safe")
	})

	builtSchema := b.MustBuild()

	q := graphql.MustParse(`
		{
			inner { inners { expensive { expensives { err } } } }
        }`, nil)

	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e := testgraphql.NewExecutorWrapper(t)
	_, err := e.Execute(context.Background(), builtSchema.Query, nil, q)
	if err == nil || err.Error() != "inner.inners.0.expensive.expensives.0.err: no good, bad" {
		t.Errorf("bad error: %v", err)
	}

	q = graphql.MustParse(`
		{
			safe
		}`, nil)

	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e = testgraphql.NewExecutorWrapper(t)
	_, err = e.Execute(context.Background(), builtSchema.Query, nil, q)
	// A client-safe error is decorated with its path like any other, so that a
	// client can tell which field failed; the message is recoverable from it.
	if err == nil || err.Error() != "safe: safe safe" {
		t.Errorf("bad error: %v", err)
	}
	if !graphql.IsSanitized(err) {
		t.Errorf("safe not safe")
	}
	if got := graphql.SanitizeError(err); got != "safe safe" {
		t.Errorf("sanitized message should survive the path decoration, got %q", got)
	}

}

func TestEnum(t *testing.T) {
	b := lightning.New()

	lightning.Enum(b, "enumType", map[string]enumType{
		"firstField":  enumType(1),
		"secondField": enumType(2),
		"thirdField":  enumType(3),
	})
	lightning.Enum(b, "enumType2", map[string]enumType2{
		"this": enumType2(1.2),
		"is":   enumType2(3.2),
		"a":    enumType2(4.3),
		"map":  enumType2(5.3),
	})

	query := b.Query()
	query.FieldArgs("inner", func(ctx context.Context, _ *lightning.Root, args enumArgs) (enumType, error) {
		return args.EnumField, nil
	})
	query.FieldArgs("inner2", func(ctx context.Context, _ *lightning.Root, args enum2Args) (enumType2, error) {
		return args.EnumField2, nil
	})
	query.FieldArgs("optional", func(ctx context.Context, _ *lightning.Root, args optionalEnumArgs) (enumType, error) {
		if args.EnumField != nil {
			return *args.EnumField, nil
		}
		return enumType(4), nil
	})
	query.FieldArgs("pointerret", func(ctx context.Context, _ *lightning.Root, args optionalEnumArgs) (*enumType, error) {
		return args.EnumField, nil
	})

	builtSchema := b.MustBuild()

	q := graphql.MustParse(`
		{
			inner(enumField: firstField)
		}
		`, nil)
	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e := testgraphql.NewExecutorWrapper(t)
	val, err := e.Execute(context.Background(), builtSchema.Query, nil, q)
	assert.Nil(t, err)
	assert.Equal(t, map[string]interface{}{
		"inner": "firstField",
	}, internal.AsJSON(val))

	q = graphql.MustParse(`
		{
			inner2(enumField2: this)
		}
		`, nil)
	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e = testgraphql.NewExecutorWrapper(t)
	val, err = e.Execute(context.Background(), builtSchema.Query, nil, q)
	assert.Nil(t, err)
	assert.Equal(t, map[string]interface{}{
		"inner2": "this",
	}, internal.AsJSON(val))

	q = graphql.MustParse(`
		{
			inner(enumField: wrongField)
		}
		`, nil)
	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err == nil {
		t.Error(err)
	}

	q = graphql.MustParse(`
		{
			optional(enumField: firstField)
		}
		`, nil)
	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e = testgraphql.NewExecutorWrapper(t)
	val, err = e.Execute(context.Background(), builtSchema.Query, nil, q)
	assert.Nil(t, err)
	assert.Equal(t, map[string]interface{}{
		"optional": "firstField",
	}, internal.AsJSON(val))

	q = graphql.MustParse(`
		{
			pointerret(enumField: firstField)
		}
		`, nil)
	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e = testgraphql.NewExecutorWrapper(t)
	val, err = e.Execute(context.Background(), builtSchema.Query, nil, q)
	assert.Nil(t, err)
	// A pointer to an enum resolves to the enum's name, as the value form does.
	// The old builder gave a *enumType field a plain number type, so the same
	// value came back as 1 through one field and "firstField" through another.
	assert.Equal(t, map[string]interface{}{
		"pointerret": "firstField",
	}, internal.AsJSON(val))

}

type enumType int32
type enumType2 float64

type enumArgs struct{ EnumField enumType }
type enum2Args struct{ EnumField2 enumType2 }
type optionalEnumArgs struct{ EnumField *enumType }

// TestEndToEndAwaitAndCache tests that slow fields get run in parallel and cached.
//
// The test verifies that the `slow` field on user, which sleeps for 100ms, gets
// run in parallel by verifying the total runtime over several users.
//
// The test verifies that a `count` sub-field of the `slow` field is cached by
// invalidating a single `slow` call, and tracking the number of calls to count.
func TestEndToEndAwaitAndCache(t *testing.T) {
	users := []*User{
		{Name: "Alice", Age: 5, resource: reactive.NewResource()},
		{Name: "Bob", Age: 6, resource: reactive.NewResource()},
		{Name: "Charlie", Age: 7, resource: reactive.NewResource()},
	}

	var mu sync.Mutex
	calls := 0

	b := lightning.New()

	user := lightning.Object[User](b)
	user.Field("slow", func(ctx context.Context, u *User) (*Slow, error) {
		reactive.AddDependency(ctx, u.resource, nil)
		time.Sleep(100 * time.Millisecond)
		return new(Slow), nil
	}).Expensive()

	slow := lightning.Object[Slow](b)
	slow.Field("count", func(ctx context.Context, _ *Slow) (bool, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return true, nil
	})

	b.Query().Field("users", func(ctx context.Context, _ *lightning.Root) ([]*User, error) {
		return users, nil
	}).Expensive()

	builtSchema := b.MustBuild()

	q := graphql.MustParse(`
		{
			users {
				name
				slow { count }
            }
        }`, nil)

	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	results := make(chan interface{})

	start := time.Now()
	rerunner := reactive.NewRerunner(context.Background(), func(ctx context.Context) (interface{}, error) {
		e := testgraphql.NewExecutorWrapper(t)
		result, err := e.Execute(ctx, builtSchema.Query, nil, q)
		if err != nil {
			t.Error(err)
		}

		results <- internal.AsJSON(result)
		return nil, nil
	}, 0, false)
	defer rerunner.Stop()

	result := <-results
	duration := time.Since(start)
	if duration > 450*time.Millisecond {
		t.Errorf("did not execute in parallel; duration %v > 150ms", duration)
	}
	if !reflect.DeepEqual(result, internal.ParseJSON(`
		{"users": [
			{"name": "Alice", "slow": {"count": true}},
			{"name": "Bob", "slow": {"count": true}},
			{"name": "Charlie", "slow": {"count": true}}
        ]}`)) {
		t.Error("bad value")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls to slow, got %d", calls)
	}

	start = time.Now()
	users[0].resource.Strobe()
	result = <-results
	duration = time.Since(start)
	if duration > 450*time.Millisecond {
		t.Errorf("did not execute in parallel; duration %v > 150ms", duration)
	}
	if !reflect.DeepEqual(result, internal.ParseJSON(`
		{"users": [
			{"name": "Alice", "slow": {"count": true}},
			{"name": "Bob", "slow": {"count": true}},
			{"name": "Charlie", "slow": {"count": true}}
        ]}`)) {
		t.Error("bad value")
	}
	if calls != 4 {
		t.Errorf("expected 4 total calls to slow, got %d", calls)
	}
}

func verifyArgumentOption(t *testing.T, query graphql.Type, queryString string, variables map[string]interface{}, expectedResult string) {
	q := graphql.MustParse(queryString, variables)

	if err := graphql.PrepareQuery(context.Background(), query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	e := testgraphql.NewExecutorWrapper(t)
	result, err := e.Execute(context.Background(), query, nil, q)
	if err != nil {
		t.Error(err)
	}

	if !reflect.DeepEqual(internal.AsJSON(result), internal.ParseJSON(expectedResult)) {
		t.Error(internal.AsJSON(result))
	}
}

// TestArgumentOptionality tests that optional arguments can be omitted from
// query variables and that mandatory arguments must be included.
func TestArgumentOptionality(t *testing.T) {
	b := lightning.New()
	query := b.Query()

	query.FieldArgs("optional", func(ctx context.Context, _ *lightning.Root, args optionalIntArgs) (int64, error) {
		if args.X != nil {
			return *args.X, nil
		}
		return -1, nil
	})
	query.FieldArgs("mandatory", func(ctx context.Context, _ *lightning.Root, args mandatoryIntArgs) (int64, error) {
		return args.X, nil
	})

	builtSchema := b.MustBuild()
	emptyVariables := map[string]interface{}{}
	filledVariables := map[string]interface{}{
		"testArg": float64(5),
	}

	// An optional argument that is passed in returns successfully.
	verifyArgumentOption(t, builtSchema.Query, `
		query getOptional($testArg: Int64) {
			optional(x: $testArg)
		}`, filledVariables, `{"optional": "5"}`)

	// An optional argument that is omitted returns successfully.
	verifyArgumentOption(t, builtSchema.Query, `
			query getOptional($testArg: Int64) {
				optional(x: $testArg)
			}`, emptyVariables, `{"optional": "-1"}`)

	// A mandatory argument that is passed in returns successfully.
	verifyArgumentOption(t, builtSchema.Query, `
		query getMandatory($testArg: Int64!) {
			mandatory(x: $testArg)
		}`, filledVariables, `{"mandatory": "5"}`)
}

type optionalIntArgs struct{ X *int64 }
type mandatoryIntArgs struct{ X int64 }

// TestConcurrencyLimiterDeadlock tests that the executor does not cause a
// concurrency limit deadlock by holding on to tokens after a resolver finishes
// running.
func TestConcurrencyLimiterDeadlock(t *testing.T) {
	var mu sync.Mutex
	calls := 0

	b := lightning.New()

	user := lightning.Object[User](b)
	user.Field("slow", func(ctx context.Context, u *User) (*Slow, error) {
		time.Sleep(10 * time.Millisecond)
		return &Slow{}, nil
	})

	slow := lightning.Object[Slow](b)
	slow.Field("count", func(ctx context.Context, _ *Slow) (bool, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return true, nil
	})

	b.Query().Field("users", func(ctx context.Context, _ *lightning.Root) ([]*User, error) {
		var users []*User
		for i := 0; i < 200; i++ {
			users = append(users, &User{})
		}
		return users, nil
	})

	builtSchema := b.MustBuild()

	q := graphql.MustParse(`
		{
			users {
				one: slow { count }
				two: slow { count }
            }
        }`, nil)

	if err := graphql.PrepareQuery(context.Background(), builtSchema.Query, q.SelectionSet); err != nil {
		t.Error(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	rerunner := reactive.NewRerunner(context.Background(), func(ctx context.Context) (interface{}, error) {
		defer wg.Done()
		e := testgraphql.NewExecutorWrapper(t)
		ctx = concurrencylimiter.With(ctx, 100)

		_, err := e.Execute(ctx, builtSchema.Query, nil, q)
		if err != nil {
			t.Error(err)
		}

		assert.Equal(t, 2*200, calls)
		return nil, nil
	}, 0, false)

	wg.Wait()
	defer rerunner.Stop()
}
