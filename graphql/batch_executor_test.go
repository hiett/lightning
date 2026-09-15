package graphql_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// batchObject is the object every case below resolves a "value" field on.
type batchObject struct {
	lightning.Meta `graphql:"Object"`

	Key string
	Num int
}

// fiveObjects and twoObjects are the lists the concurrency cases count work
// units over.
func twoObjects() []*batchObject {
	return []*batchObject{{Key: "key1"}, {Key: "key2"}}
}

func fiveObjects() []*batchObject {
	return []*batchObject{{Key: "key1"}, {Key: "key2"}, {Key: "key3"}, {Key: "key4"}, {Key: "key5"}}
}

func objectsField(b *lightning.Builder, all func() []*batchObject) {
	b.Query().Field("objects", func(ctx context.Context, _ *lightning.Root) ([]*batchObject, error) {
		return all(), nil
	})
}

// TestNonExpensiveExecution counts the work units the executor schedules.
//
// An ordinary field is resolved inline, so it costs nothing to schedule; only
// an expensive field or a batched one becomes a unit of its own. The old
// library marked every registered resolver external and so scheduled all of
// them, which is why the counts here are smaller than they used to be.
func TestNonExpensiveExecution(t *testing.T) {
	tests := []struct {
		name             string
		registrationFunc func(*lightning.Builder)
		query            string
		wantResultJSON   string
		wantError        string
		wantRuns         int64
	}{
		{
			name: "non-expensive run with single value",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, func() []*batchObject { return []*batchObject{{Key: "key1"}} })
				lightning.Object[batchObject](b).Attr("value", func(o *batchObject) *batchObject { return o })
			},
			query: `
			{
				objects {
					key
					value {
						key
					}
				}
			}`,
			wantResultJSON: `
			{"objects": [
			{"key": "key1", "value": { "key": "key1"}}
			]}
			`,
			wantRuns: 1, // one unit: an ordinary field is resolved inline
		},
		{
			name: "non-expensive run with multiple value",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, twoObjects)
				lightning.Object[batchObject](b).Attr("value", func(o *batchObject) *batchObject { return o })
			},
			query: `
			{
				objects {
					key
					value {
						key
					}
				}
			}`,
			wantResultJSON: `
			{"objects": [
			{"key": "key1", "value": { "key": "key1"}},
			{"key": "key2", "value": { "key": "key2"}}
			]}
			`,
			wantRuns: 1, // one unit: an ordinary field is resolved inline
		},
		{
			name: "expensive run with multiple value",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, twoObjects)
				lightning.Object[batchObject](b).
					Attr("value", func(o *batchObject) *batchObject { return o }).
					Expensive()
			},
			query: `
			{
				objects {
					key
					value {
						key
					}
				}
			}`,
			wantResultJSON: `
			{"objects": [
			{"key": "key1", "value": { "key": "key1"}},
			{"key": "key2", "value": { "key": "key2"}}
			]}
			`,
			wantRuns: 3, // Objects + (Value * 2 objects)
		},
		{
			name: "batch run with extra concurrency",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, fiveObjects)
				lightning.Object[batchObject](b).
					Batch("value", func(ctx context.Context, parents []*batchObject) ([]*batchObject, error) {
						assert.True(t, len(parents) > 0, "batch run with extra concurrency too few objects in batch")
						return parents, nil
					}).
					Split(func(ctx context.Context, parents int) int {
						assert.Equal(t, 5, parents, "batch run with extra concurrency invalid number of objects")
						return 2
					})
			},
			query: `
			{
				objects {
					key
					value {
						key
					}
				}
			}`,
			wantResultJSON: `
			{"objects": [
			{"key": "key1", "value": { "key": "key1"}},
			{"key": "key2", "value": { "key": "key2"}},
			{"key": "key3", "value": { "key": "key3"}},
			{"key": "key4", "value": { "key": "key4"}},
			{"key": "key5", "value": { "key": "key5"}}
			]}
			`,
			wantRuns: 3, // Objects + (Value * 2 batches of objects)
		},
		{
			name: "non-expensive run with extra concurrency",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, fiveObjects)
				lightning.Object[batchObject](b).
					Batch("value", func(ctx context.Context, parents []*batchObject) ([]*batchObject, error) {
						return parents, nil
					}).
					Split(func(ctx context.Context, parents int) int {
						assert.Equal(t, 5, parents, "non-expensive run with extra concurrency invalid number of objects")
						return 2
					})
			},
			query: `
			{
				objects {
					key
					value {
						key
					}
				}
			}`,
			wantResultJSON: `
			{"objects": [
			{"key": "key1", "value": { "key": "key1"}},
			{"key": "key2", "value": { "key": "key2"}},
			{"key": "key3", "value": { "key": "key3"}},
			{"key": "key4", "value": { "key": "key4"}},
			{"key": "key5", "value": { "key": "key5"}}
			]}
			`,
			wantRuns: 3, // Objects + (Value * 2 batches of objects)
		},
		{
			name: "batch run with extremely high concurrency",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, fiveObjects)
				lightning.Object[batchObject](b).
					Batch("value", func(ctx context.Context, parents []*batchObject) ([]*batchObject, error) {
						assert.True(t, len(parents) > 0, "batch run with extremely high concurrency too few objects in batch")
						return parents, nil
					}).
					Split(func(ctx context.Context, parents int) int {
						assert.Equal(t, 5, parents, "batch run with extremely high concurrency invalid number of objects")
						return 10 // Bigger number than value passed in
					})
			},
			query: `
			{
				objects {
					key
					value {
						key
					}
				}
			}`,
			wantResultJSON: `
			{"objects": [
			{"key": "key1", "value": { "key": "key1"}},
			{"key": "key2", "value": { "key": "key2"}},
			{"key": "key3", "value": { "key": "key3"}},
			{"key": "key4", "value": { "key": "key4"}},
			{"key": "key5", "value": { "key": "key5"}}
			]}
			`,
			wantRuns: 6, // Objects + (Value * 5 batches of objects)
		},
		{
			name: "batch run with zero concurrency",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, fiveObjects)
				lightning.Object[batchObject](b).
					Batch("value", func(ctx context.Context, parents []*batchObject) ([]*batchObject, error) {
						assert.True(t, len(parents) > 0, "batch run with zero concurrency too few objects in batch")
						return parents, nil
					}).
					Split(func(ctx context.Context, parents int) int {
						assert.Equal(t, 5, parents, "batch run with zero concurrency invalid number of objects")
						return 0 // Invalid low value
					})
			},
			query: `
			{
				objects {
					key
					value {
						key
					}
				}
			}`,
			wantResultJSON: `
			{"objects": [
			{"key": "key1", "value": { "key": "key1"}},
			{"key": "key2", "value": { "key": "key2"}},
			{"key": "key3", "value": { "key": "key3"}},
			{"key": "key4", "value": { "key": "key4"}},
			{"key": "key5", "value": { "key": "key5"}}
			]}
			`,
			wantRuns: 2, // Objects + (Value * 1 batches of objects)
		},
		{
			name: "non-expensive run with deep execution",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, twoObjects)
				lightning.Object[batchObject](b).Attr("value", func(o *batchObject) *batchObject { return o })
			},
			query: `
			{
				objects {
					key
					value {
						value {
							value {
								key
							}
						}
					}
				}
			}`,
			wantResultJSON: `
			{"objects": [
			{"key": "key1", "value": { "value": { "value": {"key": "key1"}}}},
			{"key": "key2", "value": { "value": { "value": {"key": "key2"}}}}
			]}
			`,
			wantRuns: 1, // one unit: depth costs nothing when every field is ordinary
		},
		{
			name: "expensive run with deep execution",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, twoObjects)
				lightning.Object[batchObject](b).
					Attr("value", func(o *batchObject) *batchObject { return o }).
					Expensive()
			},
			query: `
			{
				objects {
					key
					value {
						value {
							value {
								key
							}
						}
					}
				}
			}`,
			wantResultJSON: `
			{"objects": [
			{"key": "key1", "value": { "value": { "value": {"key": "key1"}}}},
			{"key": "key2", "value": { "value": { "value": {"key": "key2"}}}}
			]}
			`,
			wantRuns: 7, // Objects + ((Value + Value + Value) * 2 Objects)
		},
		{
			name: "non-expensive error",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, twoObjects)
				lightning.Object[batchObject](b).Field("value", func(ctx context.Context, o *batchObject) (*batchObject, error) {
					if o.Key == "key2" {
						return nil, errors.New("bad times")
					}
					return o, nil
				})
			},
			query: `
			{
				objects {
					key
					value {
						key
					}
				}
			}`,
			wantError: "objects.1.value: bad times",
		},
		{
			name: "non-expensive error first index",
			registrationFunc: func(b *lightning.Builder) {
				objectsField(b, twoObjects)
				lightning.Object[batchObject](b).Field("value", func(ctx context.Context, o *batchObject) (*batchObject, error) {
					if o.Key == "key1" {
						return nil, errors.New("bad times")
					}
					return o, nil
				})
			},
			query: `
			{
				objects {
					key
					value {
						key
					}
				}
			}`,
			wantError: "objects.0.value: bad times",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := lightning.New()
			tt.registrationFunc(b)

			schema, err := b.Build()
			require.NoError(t, err)

			q := graphql.MustParse(tt.query, nil)

			if err := graphql.PrepareQuery(context.Background(), schema.Query, q.SelectionSet); err != nil {
				t.Error(err)
			}

			c := &counterGoroutineScheduler{}
			e := graphql.NewExecutor(c)

			ctx := context.Background()
			res, err := e.Execute(ctx, schema.Query, nil, q)
			if tt.wantError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantError)
				return
			}
			require.NoError(t, err)

			wantParsedJSON := internal.ParseJSON(tt.wantResultJSON)
			gotJSON := internal.AsJSON(res)

			require.Equal(
				t,
				wantParsedJSON,
				gotJSON,
				"Mismatch for expected vs actual response.  Want:\n%s\nGot:\n%s",
				internal.MarshalJSON(wantParsedJSON),
				internal.MarshalJSON(gotJSON),
			)
			require.Equal(t, tt.wantRuns, c.count, "unexpected number of work units")
		})
	}
}

type counterGoroutineScheduler struct {
	wg sync.WaitGroup

	count int64
}

func (q *counterGoroutineScheduler) Run(resolver graphql.UnitResolver, initialUnits ...*graphql.WorkUnit) {
	q.runEnqueue(resolver, initialUnits...)

	q.wg.Wait()
}

func (q *counterGoroutineScheduler) runEnqueue(resolver graphql.UnitResolver, units ...*graphql.WorkUnit) {
	atomic.AddInt64(&q.count, int64(len(units)))
	for _, unit := range units {
		q.wg.Add(1)
		go func(u *graphql.WorkUnit) {
			defer q.wg.Done()
			units := resolver(u)
			q.runEnqueue(resolver, units...)
		}(unit)
	}
}
