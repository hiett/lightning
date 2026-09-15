package relay

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"sort"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
)

// This file implements Relay cursor connections.
//
// Cursors are **key-based**, not offsets: a cursor names the item it points at,
// so a page stays meaningful when the list changes underneath it. In a library
// whose headline feature is live queries over changing lists, an offset cursor
// would be wrong in a way it would not be elsewhere — insert an item near the
// front and every outstanding offset cursor silently shifts by one.
//
// The key comes from the type's Node registration, so a connection over a node
// type needs no key declaration at all. That is also what removes the old
// library's requirement that the key be an exposed field.

// Page is what a connection resolver receives: the slice of the list a client
// asked for.
type Page struct {
	// First and Last are the client's requested page size, if given.
	First *int32
	// Last is the page size counting backwards from Before.
	Last *int32
	// After and Before are cursors bounding the page, if given.
	After  *string
	Before *string
}

// Limit reports how many items the page should hold, after the plugin's cap.
func (p Page) Limit(max int32) int32 {
	switch {
	case p.First != nil && *p.First < max:
		return *p.First
	case p.Last != nil && *p.Last < max:
		return *p.Last
	default:
		return max
	}
}

// connectionArgs are the arguments every connection field takes.
type connectionArgs struct {
	First  *int32  `description:"Return the first n items."`
	Last   *int32  `description:"Return the last n items."`
	After  *string `description:"Return items after this cursor."`
	Before *string `description:"Return items before this cursor."`
}

// edge is one item of a connection, with its cursor.
type edge struct {
	node   any
	cursor string
}

// Connection declares a field that returns a Relay connection over T.
//
// The resolver returns the whole list and the plugin pages it:
//
//	relay.Connection(b.Query(), "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, error) {
//	    return store.Tasks(ctx)
//	})
//
// The element type must be a registered node, because its Node identifier is
// what the cursors are built from.
func Connection[P, T any](parent *lightning.Type[P], name string, resolve func(ctx context.Context, p *P, page Page) ([]T, error)) *lightning.Field {
	return connection(parent, name, func(ctx context.Context, p *P, page Page, _ struct{}) ([]T, error) {
		return resolve(ctx, p, page)
	})
}

// ConnectionArgs is Connection for a field that takes arguments of its own,
// alongside the pagination arguments.
func ConnectionArgs[P, T, A any](parent *lightning.Type[P], name string, resolve func(ctx context.Context, p *P, page Page, args A) ([]T, error)) *lightning.Field {
	return connection(parent, name, resolve)
}

func connection[P, T, A any](parent *lightning.Type[P], name string, resolve func(ctx context.Context, p *P, page Page, args A) ([]T, error)) *lightning.Field {
	b := parent.Builder()

	r, err := find(b)
	if err != nil {
		b.Errorf("relay.Connection: %s: %s", name, err)
		return parent.Placeholder(name)
	}

	elemType := reflect.TypeFor[T]()
	for elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
	}

	// The connection's own arguments are the pagination ones plus whatever the
	// resolver asks for, merged into one struct so the client sees one list.
	return parent.RawFieldArgs(name, mergedArgs[A](), func(ctx context.Context, source, rawArgs any, _ *graphql.SelectionSet) (any, error) {
		page, extra, err := splitArgs[A](rawArgs)
		if err != nil {
			return nil, err
		}

		p, ok := lightning.SourceAs[P](source)
		if !ok {
			return nil, nil
		}

		items, err := resolve(ctx, p, page, extra)
		if err != nil {
			return nil, err
		}

		return r.paginate(ctx, elemType, items, page)
	}, func(b *lightning.Builder) (graphql.Type, error) {
		return r.connectionType(b, elemType)
	})
}

// paginate turns a resolved list into a connection.
func (r *Relay) paginate(ctx context.Context, elemType reflect.Type, items any, page Page) (any, error) {
	node, ok := r.nodes[elemType]
	if !ok {
		return nil, fmt.Errorf("a connection over %s needs it to be a node; register it with relay.Node", typeName(elemType))
	}

	list := reflect.ValueOf(items)
	edges := make([]edge, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		value := list.Index(i).Interface()
		localID, err := node.localID(ctx, value)
		if err != nil {
			return nil, err
		}
		edges = append(edges, edge{
			node: value,
			// The cursor names the item, so it keeps meaning when the list
			// changes: base64 only so that it reads as opaque.
			cursor: base64.StdEncoding.EncodeToString([]byte(localID)),
		})
	}

	total := int64(len(edges))
	edges, hasPrevious, hasNext := applyCursors(edges, page)

	limit := int(page.Limit(r.maxPageSize))
	if page.Last != nil && page.First == nil {
		if len(edges) > limit {
			edges = edges[len(edges)-limit:]
			hasPrevious = true
		}
	} else if len(edges) > limit {
		edges = edges[:limit]
		hasNext = true
	}

	return &connectionValue{
		totalCount:      total,
		edges:           edges,
		hasNextPage:     hasNext,
		hasPreviousPage: hasPrevious,
	}, nil
}

// applyCursors narrows a list to the range the cursors bound, reporting whether
// anything lies outside it.
func applyCursors(edges []edge, page Page) ([]edge, bool, bool) {
	hasPrevious := false
	hasNext := false

	if page.After != nil {
		if i := indexOfCursor(edges, *page.After); i >= 0 {
			edges = edges[i+1:]
			hasPrevious = i >= 0
		}
	}
	if page.Before != nil {
		// The length is read here, after the After cursor has already narrowed
		// the list, so that the last remaining edge really is the last one.
		remaining := len(edges)
		if i := indexOfCursor(edges, *page.Before); i >= 0 {
			edges = edges[:i]
			hasNext = i != remaining-1
		}
	}

	return edges, hasPrevious, hasNext
}

func indexOfCursor(edges []edge, cursor string) int {
	for i, e := range edges {
		if e.cursor == cursor {
			return i
		}
	}
	return -1
}

// connectionValue is what a connection field resolves to.
type connectionValue struct {
	totalCount      int64
	edges           []edge
	hasNextPage     bool
	hasPreviousPage bool
}

// connectionType builds, once per element type, the Connection and Edge object
// types and the shared PageInfo type.
func (r *Relay) connectionType(b *lightning.Builder, elemType reflect.Type) (graphql.Type, error) {
	name := ""
	for _, declared := range b.DeclaredTypes() {
		if declared.GoType() == elemType {
			name = declared.Name()
			break
		}
	}
	if name == "" {
		return nil, fmt.Errorf("a connection over %s needs it declared with lightning.Object", typeName(elemType))
	}

	if r.connectionTypes == nil {
		r.connectionTypes = map[string]graphql.Type{}
	}
	if built, ok := r.connectionTypes[name]; ok {
		return built, nil
	}

	nodeType, err := b.BuiltType(elemType)
	if err != nil {
		return nil, err
	}

	edgeObject := &graphql.Object{
		Name:        name + "Edge",
		Description: fmt.Sprintf("An edge in a %s connection.", name),
		Fields: map[string]*graphql.Field{
			"node": {
				Type:           &graphql.NonNull{Type: unwrapNonNull(nodeType)},
				Description:    "The item at the end of the edge.",
				ParseArguments: noArguments,
				Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
					return source.(edge).node, nil
				},
			},
			"cursor": {
				Type:           &graphql.NonNull{Type: &graphql.Scalar{Type: "String"}},
				Description:    "A cursor for use in pagination.",
				ParseArguments: noArguments,
				Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
					return source.(edge).cursor, nil
				},
			},
		},
	}

	connectionObject := &graphql.Object{
		Name:        name + "Connection",
		Description: fmt.Sprintf("A paginated list of %s, following the Relay Cursor Connections specification.", name),
		Fields: map[string]*graphql.Field{
			"edges": {
				Type:           &graphql.NonNull{Type: &graphql.List{Type: &graphql.NonNull{Type: edgeObject}}},
				Description:    "The items in this page, each with its cursor.",
				ParseArguments: noArguments,
				Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
					out := make([]any, 0, len(source.(*connectionValue).edges))
					for _, e := range source.(*connectionValue).edges {
						out = append(out, e)
					}
					return out, nil
				},
			},
			"pageInfo": {
				Type:           &graphql.NonNull{Type: pageInfoType()},
				Description:    "Where this page sits in the whole list.",
				ParseArguments: noArguments,
				Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
					return source, nil
				},
			},
			"totalCount": {
				Type:           &graphql.NonNull{Type: &graphql.Scalar{Type: "Int64", Unwrapper: int64AsString}},
				Description:    "How many items the whole list holds. An extension; Relay ignores it.",
				ParseArguments: noArguments,
				Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
					return source.(*connectionValue).totalCount, nil
				},
			},
		},
	}

	built := &graphql.NonNull{Type: connectionObject}
	r.connectionTypes[name] = built
	return built, nil
}

// pageInfoType is the shared PageInfo object.
func pageInfoType() *graphql.Object {
	boolean := func(read func(*connectionValue) bool, description string) *graphql.Field {
		return &graphql.Field{
			Type:           &graphql.NonNull{Type: &graphql.Scalar{Type: "Boolean"}},
			Description:    description,
			ParseArguments: noArguments,
			Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
				return read(source.(*connectionValue)), nil
			},
		}
	}
	cursor := func(read func(*connectionValue) *string, description string) *graphql.Field {
		return &graphql.Field{
			Type:           &graphql.Scalar{Type: "String"},
			Description:    description,
			ParseArguments: noArguments,
			Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
				value := read(source.(*connectionValue))
				if value == nil {
					return nil, nil
				}
				return *value, nil
			},
		}
	}

	return &graphql.Object{
		Name:        "PageInfo",
		Description: "Where a page sits in the whole list.",
		Fields: map[string]*graphql.Field{
			"hasNextPage": boolean(func(c *connectionValue) bool { return c.hasNextPage },
				"Whether more items follow this page."),
			"hasPreviousPage": boolean(func(c *connectionValue) bool { return c.hasPreviousPage },
				"Whether more items precede this page."),
			"startCursor": cursor(func(c *connectionValue) *string {
				if len(c.edges) == 0 {
					return nil
				}
				return &c.edges[0].cursor
			}, "The cursor of the first item in this page, or null if it is empty."),
			"endCursor": cursor(func(c *connectionValue) *string {
				if len(c.edges) == 0 {
					return nil
				}
				return &c.edges[len(c.edges)-1].cursor
			}, "The cursor of the last item in this page, or null if it is empty."),
		},
	}
}

func int64AsString(source any) (any, error) {
	return fmt.Sprintf("%d", source.(int64)), nil
}

func unwrapNonNull(t graphql.Type) graphql.Type {
	if nn, ok := t.(*graphql.NonNull); ok {
		return nn.Type
	}
	return t
}

// mergedArgs returns the Go type holding the pagination arguments alongside the
// resolver's own.
func mergedArgs[A any]() reflect.Type {
	extra := reflect.TypeFor[A]()
	fields := []reflect.StructField{
		{Name: "First", Type: reflect.TypeFor[*int32](), Tag: `description:"Return the first n items."`},
		{Name: "Last", Type: reflect.TypeFor[*int32](), Tag: `description:"Return the last n items."`},
		{Name: "After", Type: reflect.TypeFor[*string](), Tag: `description:"Return items after this cursor."`},
		{Name: "Before", Type: reflect.TypeFor[*string](), Tag: `description:"Return items before this cursor."`},
	}

	if extra.Kind() == reflect.Struct {
		for i := 0; i < extra.NumField(); i++ {
			field := extra.Field(i)
			if field.PkgPath != "" {
				continue
			}
			fields = append(fields, field)
		}
	}

	return reflect.StructOf(fields)
}

// splitArgs separates the pagination arguments from the resolver's own.
func splitArgs[A any](raw any) (Page, A, error) {
	var extra A

	value := reflect.ValueOf(raw)
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return Page{}, extra, fmt.Errorf("arguments are %T, expected a struct", raw)
	}

	page := Page{
		First:  value.FieldByName("First").Interface().(*int32),
		Last:   value.FieldByName("Last").Interface().(*int32),
		After:  value.FieldByName("After").Interface().(*string),
		Before: value.FieldByName("Before").Interface().(*string),
	}

	extraType := reflect.TypeFor[A]()
	if extraType.Kind() == reflect.Struct && extraType.NumField() > 0 {
		out := reflect.New(extraType).Elem()
		for i := 0; i < extraType.NumField(); i++ {
			field := extraType.Field(i)
			if field.PkgPath != "" {
				continue
			}
			out.Field(i).Set(value.FieldByName(field.Name))
		}
		extra = out.Interface().(A)
	}

	return page, extra, nil
}

var _ = sort.Strings
