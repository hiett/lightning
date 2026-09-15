package relay

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
)

// This file builds the Node interface and the node and nodes root fields.

// GID is a decoded global identifier, usable as an argument type.
//
// An argument declared as a GID arrives decoded: the resolver receives the type
// name and the type-local identifier, instead of every application writing the
// same decode by hand against a codec that then drifts from the schema's.
type GID struct {
	// Type is the GraphQL type name the identifier names.
	Type string
	// Local is the type-local identifier.
	Local string
}

// String returns the local identifier, which is what a store usually wants.
func (g GID) String() string { return g.Local }

// nodeInterface finds or creates the Node interface in a built schema.
func (r *Relay) nodeInterface(schema *graphql.Schema) *graphql.Interface {
	query, ok := schema.Query.(*graphql.Object)
	if !ok {
		return nil
	}
	field, ok := query.Fields["node"]
	if !ok {
		return nil
	}
	iface, _ := field.Type.(*graphql.Interface)
	return iface
}

// addRootFields adds node(id:) and nodes(ids:) to the query root.
func (r *Relay) addRootFields(b *lightning.Builder) {
	iface := &graphql.Interface{
		Name:          NodeTypeName,
		Description:   "An object with a globally unique identifier, which a client can refetch with the node field.",
		Fields:        map[string]*graphql.Field{},
		PossibleTypes: map[string]*graphql.Object{},
	}

	iface.Fields["id"] = &graphql.Field{
		Type:           &graphql.NonNull{Type: idScalar()},
		Description:    "A globally unique identifier.",
		ParseArguments: noArguments,
		Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
			node, err := r.nodeFor(source)
			if err != nil {
				return nil, err
			}
			localID, err := node.localID(ctx, source)
			if err != nil {
				return nil, err
			}
			return r.codec.Encode(node.name, localID)
		},
	}

	iface.TypeResolver = func(value any) (string, any, error) {
		if value == nil {
			return "", nil, nil
		}
		node, err := r.nodeFor(value)
		if err != nil {
			return "", nil, err
		}
		return node.name, value, nil
	}

	b.RootField(lightning.QueryRoot, "node", &graphql.Field{
		Type:            iface,
		Description:     "Looks up a single object by its global identifier.",
		Args:            map[string]graphql.Type{"id": &graphql.NonNull{Type: idScalar()}},
		ArgDescriptions: map[string]string{"id": "The object's global identifier."},
		ParseArguments:  parseOneID,
		Expensive:       true,
		Resolve: func(ctx context.Context, _, args any, _ *graphql.SelectionSet) (any, error) {
			return r.resolveOne(ctx, args.(string))
		},
	})

	b.RootField(lightning.QueryRoot, "nodes", &graphql.Field{
		Type:            &graphql.NonNull{Type: &graphql.List{Type: iface}},
		Description:     "Looks up several objects by their global identifiers, in the order given.",
		Args:            map[string]graphql.Type{"ids": &graphql.NonNull{Type: &graphql.List{Type: &graphql.NonNull{Type: idScalar()}}}},
		ArgDescriptions: map[string]string{"ids": "The objects' global identifiers."},
		ParseArguments:  parseIDList,
		Expensive:       true,
		Resolve: func(ctx context.Context, _, args any, _ *graphql.SelectionSet) (any, error) {
			ids := args.([]string)
			out := make([]any, 0, len(ids))
			for _, id := range ids {
				value, err := r.resolveOne(ctx, id)
				if err != nil {
					return nil, err
				}
				out = append(out, value)
			}
			return out, nil
		},
	})
}

// nodeFor finds the registration for a resolved value's Go type.
func (r *Relay) nodeFor(value any) (*nodeType, error) {
	goType := derefType(value)
	if node, ok := r.nodes[goType]; ok {
		return node, nil
	}

	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("%s is not a registered node type; the node types are %v", typeName(goType), names)
}

// resolveOne decodes a global identifier and fetches what it names.
func (r *Relay) resolveOne(ctx context.Context, globalID string) (any, error) {
	typeName, localID, err := r.codec.Decode(globalID)
	if err != nil {
		return nil, graphql.NewClientError("%s", err.Error())
	}

	node, ok := r.byName[typeName]
	if !ok {
		return nil, graphql.NewClientError("unknown node type %q", typeName)
	}

	value, err := node.fetch(ctx, localID)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	return value, nil
}

func parseOneID(raw any) (any, error) {
	args, _ := raw.(map[string]any)
	value, ok := args["id"]
	if !ok || value == nil {
		return nil, fmt.Errorf("id: required argument missing")
	}
	id, err := asID(value)
	if err != nil {
		return nil, fmt.Errorf("id: %s", err)
	}
	for name := range args {
		if name != "id" {
			return nil, fmt.Errorf("unknown argument %q", name)
		}
	}
	return id, nil
}

func parseIDList(raw any) (any, error) {
	args, _ := raw.(map[string]any)
	value, ok := args["ids"]
	if !ok || value == nil {
		return nil, fmt.Errorf("ids: required argument missing")
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("ids: expected a list of IDs")
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		id, err := asID(item)
		if err != nil {
			return nil, fmt.Errorf("ids[%d]: %s", i, err)
		}
		out = append(out, id)
	}
	for name := range args {
		if name != "ids" {
			return nil, fmt.Errorf("unknown argument %q", name)
		}
	}
	return out, nil
}

func asID(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case float64:
		if typed != float64(int64(typed)) {
			return "", fmt.Errorf("not a valid ID")
		}
		return fmt.Sprintf("%d", int64(typed)), nil
	case int64:
		return fmt.Sprintf("%d", typed), nil
	default:
		return "", fmt.Errorf("not a valid ID")
	}
}

// derefType returns the struct type behind a value, following pointers.
func derefType(value any) reflect.Type {
	t := reflect.TypeOf(value)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}
