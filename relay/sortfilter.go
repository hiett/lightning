package relay

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/internal/filter"
)

// This file orders and searches a connection.
//
// Which fields a list can be ordered by, and which fields a text search reads,
// are facts about the node type rather than about the list: whether a task's
// title can be searched is a property of the title. So they are declared on the
// field, next to it:
//
//	type Task struct {
//	    Title string `description:"What needs doing." sortable:"true" filterable:"true"`
//	}
//
// and the arguments follow from that. A connection over a type with nothing
// sortable has no sortBy argument at all — which is the fix for the old
// library, where every connection advertised sortBy, sortOrder, filterText,
// filterTextFields and filterType whether or not anything was registered, and
// silently did nothing when a client used them.

// SortOrder says which way a sorted connection runs.
type SortOrder int32

const (
	// Ascending orders a connection from smallest to largest.
	Ascending SortOrder = iota
	// Descending orders it from largest to smallest.
	Descending
)

// sortFilter is what a connection over one node type can be ordered and
// searched by, worked out once the node type is built.
type sortFilter struct {
	node       *graphql.Object
	sortable   []string
	filterable []string
}

// sortFilterFor reads a node type's sortable and filterable fields.
func sortFilterFor(b *lightning.Builder, elemType reflect.Type) (*sortFilter, error) {
	built, err := b.BuiltType(elemType)
	if err != nil {
		return nil, err
	}
	object, ok := unwrapNonNull(built).(*graphql.Object)
	if !ok {
		return nil, fmt.Errorf("%s is not an object type", typeName(elemType))
	}

	spec := &sortFilter{node: object}
	for _, declared := range b.DeclaredTypes() {
		if declared.GoType() != elemType {
			continue
		}
		spec.sortable = declared.SortableFields()
		spec.filterable = declared.FilterableFields()
		break
	}

	// A field that is asked for once per node cannot also be asked a question,
	// because a sort has no selection to read the arguments from.
	for _, name := range append(append([]string(nil), spec.sortable...), spec.filterable...) {
		field, ok := object.Fields[name]
		if !ok {
			return nil, fmt.Errorf("%s has no field %s to sort or filter by", object.Name, name)
		}
		if len(field.Args) > 0 {
			return nil, fmt.Errorf("%s.%s takes arguments, so a connection cannot sort or filter by it", object.Name, name)
		}
	}

	return spec, nil
}

// sortsOrFilters reports whether the node type offers either.
func (s *sortFilter) sortsOrFilters() bool {
	return s != nil && (len(s.sortable) > 0 || len(s.filterable) > 0)
}

// argFields are the arguments a connection gains from what its node type
// declared. Nothing sortable means no sortBy, and nothing filterable means no
// filterText: an argument that cannot do anything is not offered.
func (s *sortFilter) argFields() []reflect.StructField {
	var fields []reflect.StructField
	if len(s.sortable) > 0 {
		fields = append(fields,
			reflect.StructField{
				Name: "SortBy",
				Type: reflect.TypeFor[*string](),
				Tag:  reflect.StructTag(fmt.Sprintf("description:%q", "Order by one of: "+strings.Join(s.sortable, ", ")+".")),
			},
			reflect.StructField{
				Name: "SortOrder",
				Type: reflect.TypeFor[*SortOrder](),
				Tag:  `description:"Which way round to order. Ascending by default."`,
			},
		)
	}
	if len(s.filterable) > 0 {
		fields = append(fields,
			reflect.StructField{
				Name: "FilterText",
				Type: reflect.TypeFor[*string](),
				Tag:  reflect.StructTag(fmt.Sprintf("description:%q", "Keep only items matching this text, searching "+strings.Join(s.filterable, ", ")+". Quote a phrase to match it whole.")),
			},
			reflect.StructField{
				Name: "FilterTextFields",
				Type: reflect.TypeFor[*[]string](),
				Tag:  `description:"Which fields filterText searches. All of them by default."`,
			},
		)
	}
	return fields
}

// readSearch pulls the sort and filter arguments out of the merged struct.
func readSearch(value reflect.Value, page *Page) {
	if field := value.FieldByName("SortBy"); field.IsValid() {
		page.SortBy, _ = field.Interface().(*string)
	}
	if field := value.FieldByName("SortOrder"); field.IsValid() {
		if order, _ := field.Interface().(*SortOrder); order != nil {
			page.SortOrder = *order
		}
	}
	if field := value.FieldByName("FilterText"); field.IsValid() {
		page.FilterText, _ = field.Interface().(*string)
	}
	if field := value.FieldByName("FilterTextFields"); field.IsValid() {
		if names, _ := field.Interface().(*[]string); names != nil {
			page.FilterTextFields = *names
		}
	}
}

// apply narrows and orders a list before it is paged.
//
// Filtering comes first so that totalCount and the cursors describe the list
// the client asked about rather than the one behind it.
func (s *sortFilter) apply(ctx context.Context, nodes []any, want Page) ([]any, error) {
	nodes, err := s.filter(ctx, nodes, want)
	if err != nil {
		return nil, err
	}
	return s.sort(ctx, nodes, want)
}

// filter keeps the nodes whose searchable text matches.
func (s *sortFilter) filter(ctx context.Context, nodes []any, want Page) ([]any, error) {
	if want.FilterText == nil || len(s.filterable) == 0 {
		return nodes, nil
	}

	tokens := filter.GetDefaultSearchTokens(*want.FilterText)
	if len(tokens) == 0 {
		return nodes, nil
	}

	names := s.filterable
	if want.FilterTextFields != nil {
		names = nil
		for _, asked := range want.FilterTextFields {
			if !contains(s.filterable, asked) {
				// A client error, so the message survives sanitisation: this is
				// a mistake in the query, and the client is the only one who
				// can fix it.
				return nil, graphql.NewClientError("%s cannot be filtered by %q; it must be one of %s", s.node.Name, asked, quoted(s.filterable))
			}
			names = append(names, asked)
		}
	}

	// One column of text per searchable field, so that a batched field is asked
	// once for the whole list rather than once per node.
	columns := make([][]string, 0, len(names))
	for _, name := range names {
		column, err := s.textColumn(ctx, s.node.Fields[name], name, nodes)
		if err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}

	kept := make([]any, 0, len(nodes))
	for i, node := range nodes {
		for _, column := range columns {
			if filter.DefaultFilterFunc(column[i], tokens) {
				kept = append(kept, node)
				break
			}
		}
	}
	return kept, nil
}

// textColumn resolves one searchable field for every node.
func (s *sortFilter) textColumn(ctx context.Context, field *graphql.Field, name string, nodes []any) ([]string, error) {
	values, err := s.column(ctx, field, nodes)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(values))
	for i, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s.%s is %T, and only a string field can be filtered by text", s.node.Name, name, value)
		}
		out[i] = text
	}
	return out, nil
}

// column resolves one field for every node, batching when the field batches.
func (s *sortFilter) column(ctx context.Context, field *graphql.Field, nodes []any) ([]any, error) {
	if field.Batch && field.UseBatchFunc != nil && field.UseBatchFunc(ctx) {
		return graphql.SafeExecuteBatchResolver(ctx, field, nodes, nil, nil)
	}
	out := make([]any, len(nodes))
	for i, node := range nodes {
		value, err := graphql.SafeExecuteResolver(ctx, field, node, nil, nil)
		if err != nil {
			return nil, err
		}
		out[i] = value
	}
	return out, nil
}

// sort orders the nodes by the field the client named.
func (s *sortFilter) sort(ctx context.Context, nodes []any, want Page) ([]any, error) {
	if want.SortBy == nil || len(s.sortable) == 0 {
		return nodes, nil
	}
	if !contains(s.sortable, *want.SortBy) {
		return nil, graphql.NewClientError("%s cannot be sorted by %q; it must be one of %s", s.node.Name, *want.SortBy, quoted(s.sortable))
	}

	values, err := s.column(ctx, s.node.Fields[*want.SortBy], nodes)
	if err != nil {
		return nil, err
	}

	order := make([]int, len(nodes))
	for i := range order {
		order[i] = i
	}

	var failure error
	// A stable sort, so that equal values keep the order the resolver gave them
	// and a cursor into a sorted list means the same thing twice. Descending
	// asks the same question the other way round rather than reversing the
	// result, which would also reverse the ties.
	sort.SliceStable(order, func(a, b int) bool {
		first, second := values[order[a]], values[order[b]]
		if want.SortOrder == Descending {
			first, second = second, first
		}
		less, err := lessThan(first, second)
		if err != nil && failure == nil {
			failure = fmt.Errorf("%s.%s: %w", s.node.Name, *want.SortBy, err)
		}
		return less
	})
	if failure != nil {
		return nil, failure
	}

	out := make([]any, len(nodes))
	for i, from := range order {
		out[i] = nodes[from]
	}
	return out, nil
}

// lessThan compares two resolved sort values.
func lessThan(a, b any) (bool, error) {
	left, right := reflect.ValueOf(a), reflect.ValueOf(b)
	for left.Kind() == reflect.Ptr {
		if left.IsNil() {
			return !isNilValue(right), nil
		}
		left = left.Elem()
	}
	for right.Kind() == reflect.Ptr {
		if right.IsNil() {
			return false, nil
		}
		right = right.Elem()
	}
	if !left.IsValid() || !right.IsValid() {
		return !left.IsValid() && right.IsValid(), nil
	}

	switch left.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return left.Int() < right.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return left.Uint() < right.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return left.Float() < right.Float(), nil
	case reflect.String:
		return left.String() < right.String(), nil
	case reflect.Bool:
		return !left.Bool() && right.Bool(), nil
	}
	return false, fmt.Errorf("a %s cannot be ordered", left.Kind())
}

func isNilValue(v reflect.Value) bool {
	return !v.IsValid() || (v.Kind() == reflect.Ptr && v.IsNil())
}

func contains(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func quoted(names []string) string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = fmt.Sprintf("%q", name)
	}
	return strings.Join(out, ", ")
}
