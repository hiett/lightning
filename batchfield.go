package lightning

import (
	"context"
	"fmt"
	"reflect"

	"github.com/hiett/lightning/graphql"
)

// This file declares fields that resolve for many parents at once.
//
// The N+1 problem is what batching exists for: a list of a hundred tasks whose
// owner field runs a hundred queries. A batch field is handed every parent the
// executor is about to ask, and answers them together.
//
//	task.Batch("owner", func(ctx context.Context, tasks []*Task) ([]*User, error) {
//	    return store.UsersFor(ctx, tasks)
//	})
//
// The results line up with the parents by position, which is the contract the
// executor already has and the one every loader library uses. Returning the
// wrong number of them is reported by name rather than silently misaligning the
// response.
//
// Everything else is unchanged: R still decides the field's GraphQL type and
// its nullability, arguments still come from a struct, and the field is still
// documented the same way. Batching is how the field is resolved, not a
// different kind of field.

// Batch declares a field resolved for many parents at once.
//
// R is the type of one result, not of the slice: returning []*User gives a
// nullable User, []User gives User!, exactly as Field does.
//
// The resolver must return one result per parent, in the same order.
func (t *Type[T]) Batch[R any](name string, resolve func(ctx context.Context, parents []*T) ([]R, error)) *Field {
	return t.declareBatch(name, reflect.TypeFor[R](), nil, func(ctx context.Context, parents []*T, _ any) ([]R, error) {
		return resolve(ctx, parents)
	})
}

// BatchArgs declares a batch field that takes arguments.
//
// The arguments are the same for every parent in the batch: they came from one
// selection in the query, which is the same selection for every parent.
func (t *Type[T]) BatchArgs[R, A any](name string, resolve func(ctx context.Context, parents []*T, args A) ([]R, error)) *Field {
	argsType := reflect.TypeFor[A]()
	return t.declareBatch(name, reflect.TypeFor[R](), argsType, func(ctx context.Context, parents []*T, raw any) ([]R, error) {
		args, ok := raw.(A)
		if !ok {
			return nil, fmt.Errorf("%s: arguments are %T, expected %s", name, raw, typeName(argsType))
		}
		return resolve(ctx, parents, args)
	})
}

// Load declares a field fetched by key, which is what batching is usually for.
//
// key reads the key off a parent and load fetches a result for each distinct
// key. Repeated keys are asked for once and the answer handed to every parent
// that wanted it, so a list of tasks all owned by the same person costs one
// lookup:
//
//	task.Load("owner", func(t *Task) string { return t.OwnerID }, store.UsersByID)
//
// That is the whole declaration. The keys are deduplicated, the results are
// distributed, and the field's type comes from what load returns.
//
// load must return one result per key, in the same order.
func (t *Type[T]) Load[K comparable, R any](name string, key func(parent *T) K, load func(ctx context.Context, keys []K) ([]R, error)) *Field {
	return t.declareBatch(name, reflect.TypeFor[R](), nil, func(ctx context.Context, parents []*T, _ any) ([]R, error) {
		keys := make([]K, 0, len(parents))
		at := make([]int, len(parents))
		seen := make(map[K]int, len(parents))
		for i, parent := range parents {
			k := key(parent)
			pos, known := seen[k]
			if !known {
				pos = len(keys)
				seen[k] = pos
				keys = append(keys, k)
			}
			at[i] = pos
		}

		loaded, err := load(ctx, keys)
		if err != nil {
			return nil, err
		}
		if len(loaded) != len(keys) {
			return nil, fmt.Errorf("%s: asked for %d keys and got %d results", name, len(keys), len(loaded))
		}

		out := make([]R, len(parents))
		for i, pos := range at {
			out[i] = loaded[pos]
		}
		return out, nil
	})
}

// UseBatch decides per request whether the field batches.
//
// With batching off the same resolver runs once per parent, so there is nothing
// to write twice and nothing that can drift apart: a batch of one is a batch.
// It is the switch to reach for when batching is being rolled out, or when a
// caller is known to be asking for a single value.
func (f *Field) UseBatch(when func(ctx context.Context) bool) *Field {
	if f.decl.batchResolve == nil {
		f.b.errorf("%s.%s: UseBatch is for a field declared with Batch, BatchArgs or Load", f.parent.name, f.decl.name)
		return f
	}
	f.decl.useBatch = when
	return f
}

// declareBatch records a batch field, deriving its single-parent resolver from
// the batch one so that both paths are the same code.
func (t *Type[T]) declareBatch[R any](name string, goResult, goArgs reflect.Type, resolve func(ctx context.Context, parents []*T, args any) ([]R, error)) *Field {
	batch := func(ctx context.Context, sources []any, args any, _ *graphql.SelectionSet) ([]any, error) {
		// The executor filters nil sources out before it gets here, so every
		// source should convert; one that does not is answered with null rather
		// than being handed to the resolver as a zero value.
		parents := make([]*T, 0, len(sources))
		at := make([]int, 0, len(sources))
		for i, source := range sources {
			parent, ok := sourceAs[T](source)
			if !ok {
				continue
			}
			parents = append(parents, parent)
			at = append(at, i)
		}

		results, err := resolve(ctx, parents, args)
		if err != nil {
			return nil, err
		}
		if len(results) != len(parents) {
			return nil, fmt.Errorf("%s.%s: the batch resolver was given %d parents and returned %d results", t.decl.name, name, len(parents), len(results))
		}

		out := make([]any, len(sources))
		for i, idx := range at {
			out[idx] = results[i]
		}
		return out, nil
	}

	single := func(ctx context.Context, source, args any, _ *graphql.SelectionSet) (any, error) {
		parent, ok := sourceAs[T](source)
		if !ok {
			return nil, nil
		}
		results, err := resolve(ctx, []*T{parent}, args)
		if err != nil {
			return nil, err
		}
		if len(results) != 1 {
			return nil, fmt.Errorf("%s.%s: the batch resolver was given 1 parent and returned %d results", t.decl.name, name, len(results))
		}
		return results[0], nil
	}

	field := t.declareFieldAt(callSite(3), name, goResult, goArgs, single)
	field.decl.batchResolve = batch
	return field
}

// alwaysBatch is the default for a batch field: a field declared as batched
// batches.
func alwaysBatch(context.Context) bool { return true }
