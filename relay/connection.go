package relay

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"

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

	// SortBy names the field the client asked to order by, and SortOrder which
	// way round. They are set only when the node type declares something
	// sortable, and a plain Connection applies them itself — a resolver reads
	// them to push the work down to wherever the list comes from, and doing so
	// changes nothing, because ordering an ordered list is a no-op.
	SortBy    *string
	SortOrder SortOrder

	// FilterText is the text the client asked to search for, and
	// FilterTextFields restricts which of the node type's filterable fields it
	// searches. As with the sort, a plain Connection applies them itself.
	FilterText       *string
	FilterTextFields []string
}

// size is the page size the client asked for, or zero if it asked for none.
func (p Page) size() int {
	switch {
	case p.First != nil:
		return int(*p.First)
	case p.Last != nil:
		return int(*p.Last)
	default:
		return 0
	}
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
	return connection(parent, name, func(ctx context.Context, p *P, page Page, _ struct{}) ([]T, *PageResult, error) {
		items, err := resolve(ctx, p, page)
		return items, nil, err
	})
}

// ConnectionArgs is Connection for a field that takes arguments of its own,
// alongside the pagination arguments.
func ConnectionArgs[P, T, A any](parent *lightning.Type[P], name string, resolve func(ctx context.Context, p *P, page Page, args A) ([]T, error)) *lightning.Field {
	return connection(parent, name, func(ctx context.Context, p *P, page Page, args A) ([]T, *PageResult, error) {
		items, err := resolve(ctx, p, page, args)
		return items, nil, err
	})
}

// PageResult is what a resolver that pages the list itself reports back about
// the page it returned.
//
// It exists because a resolver that pages in the database knows things the
// plugin cannot see: how many rows matched, and whether another page follows.
// Asking for one extra row is the usual way to find out.
type PageResult struct {
	// TotalCount is how many items the whole list holds, before paging.
	TotalCount int64
	// HasNextPage and HasPreviousPage say whether anything lies beyond this
	// page in either direction.
	HasNextPage     bool
	HasPreviousPage bool
	// Pages optionally lists the cursor each page of the whole list starts at.
	Pages []string
}

// ManualConnection declares a connection whose resolver does its own paging.
//
// The resolver is given the page the client asked for and returns exactly the
// items in it, along with what it knows about the whole list. The plugin adds
// the cursors and nothing else: it does not narrow, order or trim what came
// back, because a resolver that paged in the database has already done so.
//
//	relay.ManualConnection(q, "tasks", func(ctx context.Context, _ *lightning.Root, p relay.Page) ([]*Task, relay.PageResult, error) {
//	    rows, total, more := store.PageTasks(ctx, p)
//	    return rows, relay.PageResult{TotalCount: total, HasNextPage: more}, nil
//	})
func ManualConnection[P, T any](parent *lightning.Type[P], name string, resolve func(ctx context.Context, p *P, page Page) ([]T, PageResult, error)) *lightning.Field {
	return connection(parent, name, func(ctx context.Context, p *P, page Page, _ struct{}) ([]T, *PageResult, error) {
		items, result, err := resolve(ctx, p, page)
		return items, &result, err
	})
}

// ManualConnectionArgs is ManualConnection for a field that takes arguments of
// its own.
func ManualConnectionArgs[P, T, A any](parent *lightning.Type[P], name string, resolve func(ctx context.Context, p *P, page Page, args A) ([]T, PageResult, error)) *lightning.Field {
	return connection(parent, name, func(ctx context.Context, p *P, page Page, args A) ([]T, *PageResult, error) {
		items, result, err := resolve(ctx, p, page, args)
		return items, &result, err
	})
}

func connection[P, T, A any](parent *lightning.Type[P], name string, resolve func(ctx context.Context, p *P, page Page, args A) ([]T, *PageResult, error)) *lightning.Field {
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

	// What a connection can be ordered and searched by comes from the node
	// type, so it is not known until the node type has been built.
	var spec *sortFilter

	// The connection's own arguments are the pagination ones, whatever the node
	// type declared sortable or filterable, and whatever the resolver asks for,
	// merged into one struct so the client sees one list.
	return parent.RawFieldArgsOf(name, func(b *lightning.Builder) (reflect.Type, error) {
		found, err := sortFilterFor(b, elemType)
		if err != nil {
			return nil, err
		}
		spec = found
		return mergedArgs[A](name, spec)
	}, func(ctx context.Context, source, rawArgs any, _ *graphql.SelectionSet) (any, error) {
		page, extra, err := splitArgs[A](rawArgs)
		if err != nil {
			return nil, err
		}

		p, ok := lightning.SourceAs[P](source)
		if !ok {
			return nil, nil
		}

		items, result, err := resolve(ctx, p, page, extra)
		if err != nil {
			return nil, err
		}

		return r.paginate(ctx, elemType, items, page, spec, result)
	}, func(b *lightning.Builder) (graphql.Type, error) {
		return r.connectionType(b, elemType)
	})
}

// paginate turns a resolved list into a connection.
func (r *Relay) paginate(ctx context.Context, elemType reflect.Type, items any, page Page, spec *sortFilter, manual *PageResult) (any, error) {
	edges, err := r.edgesOf(ctx, elemType, items, page, spec, manual == nil)
	if err != nil {
		return nil, err
	}

	// A resolver that paged the list itself has already answered every question
	// the plugin would ask by inspecting the list, and it can answer them about
	// the whole list rather than only the part that came back.
	if manual != nil {
		return &connectionValue{
			totalCount:      manual.TotalCount,
			edges:           edges,
			pages:           orEmpty(manual.Pages),
			hasNextPage:     manual.HasNextPage,
			hasPreviousPage: manual.HasPreviousPage,
		}, nil
	}

	total := int64(len(edges))
	pages := pagesFromEdges(edges, page.size())
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
		pages:           pages,
		hasNextPage:     hasNext,
		hasPreviousPage: hasPrevious,
	}, nil
}

// orEmpty turns a nil list into an empty one, because the schema says the field
// is a list and never null.
func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// edgesOf narrows, orders and labels a resolved list.
func (r *Relay) edgesOf(ctx context.Context, elemType reflect.Type, items any, page Page, spec *sortFilter, narrow bool) ([]edge, error) {
	node, ok := r.nodes[elemType]
	if !ok {
		return nil, fmt.Errorf("a connection over %s needs it to be a node; register it with relay.Node", typeName(elemType))
	}

	list := reflect.ValueOf(items)
	nodes := make([]any, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		nodes = append(nodes, list.Index(i).Interface())
	}

	if narrow && spec.sortsOrFilters() {
		var err error
		nodes, err = spec.apply(ctx, nodes, page)
		if err != nil {
			return nil, err
		}
	}

	edges := make([]edge, 0, len(nodes))
	for _, value := range nodes {
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
	return edges, nil
}

// pagesFromEdges lists the cursor each page of the whole list starts at, so a
// client can offer page numbers.
//
// The first entry is the empty cursor, meaning the start of the list. It is an
// extension rather than part of the Relay specification, and a Relay client
// ignores it.
func pagesFromEdges(edges []edge, size int) []string {
	if len(edges) == 0 {
		return []string{}
	}
	pages := []string{""}
	if size <= 0 {
		return pages
	}
	for i := 0; i+1 < len(edges); i++ {
		if (i+1)%size == 0 {
			pages = append(pages, edges[i].cursor)
		}
	}
	return pages
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
	pages           []string
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
				Type:           &graphql.NonNull{Type: lightning.ScalarType("Int64")},
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
			"pages": {
				Type:           &graphql.NonNull{Type: &graphql.List{Type: &graphql.NonNull{Type: &graphql.Scalar{Type: "String"}}}},
				Description:    "The cursor each page of the whole list starts at, for page-number pagination. An extension; Relay ignores it.",
				ParseArguments: noArguments,
				Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
					return source.(*connectionValue).pages, nil
				},
			},
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
func mergedArgs[A any](name string, spec *sortFilter) (reflect.Type, error) {
	extra := reflect.TypeFor[A]()
	fields := []reflect.StructField{
		{Name: "First", Type: reflect.TypeFor[*int32](), Tag: `description:"Return the first n items."`},
		{Name: "Last", Type: reflect.TypeFor[*int32](), Tag: `description:"Return the last n items."`},
		{Name: "After", Type: reflect.TypeFor[*string](), Tag: `description:"Return items after this cursor."`},
		{Name: "Before", Type: reflect.TypeFor[*string](), Tag: `description:"Return items before this cursor."`},
	}
	fields = append(fields, spec.argFields()...)

	taken := make(map[string]bool, len(fields))
	for _, field := range fields {
		taken[field.Name] = true
	}

	if extra.Kind() == reflect.Struct {
		for i := 0; i < extra.NumField(); i++ {
			field := extra.Field(i)
			if field.PkgPath != "" {
				continue
			}
			// The connection's own arguments and the resolver's share one
			// struct, so a name used twice would be two arguments of one name.
			if taken[field.Name] {
				return nil, fmt.Errorf("%s: the argument %s is already a connection argument; name it something else", name, field.Name)
			}
			taken[field.Name] = true
			fields = append(fields, field)
		}
	}

	return reflect.StructOf(fields), nil
}

// splitArgs separates the pagination arguments, the search arguments and the
// resolver's own.
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

	readSearch(value, &page)
	return page, extra, nil
}
