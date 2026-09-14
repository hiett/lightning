package schemabuilder

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hiett/lightning/graphql"
)

// This file implements Relay's Node interface: `interface Node { id: ID! }`,
// a `node(id: ID!): Node` root field, and the global identifiers that make a
// client's store keyed by `id` work.

// NodeTypeName is the name of the Relay Node interface.
const NodeTypeName = "Node"

// NodeDescription documents the Node interface in introspection and SDL.
const NodeDescription = "An object with a globally unique identifier, which a client can refetch with the node field."

// GlobalIDCodec turns a type name and a type-local identifier into the single
// opaque string a client sees, and back again.
//
// Implement it to change the wire form — to sign identifiers, say, or to keep
// type names out of them. Set it with (*Schema).SetGlobalIDCodec.
type GlobalIDCodec interface {
	// Encode builds the global identifier for a value of typeName whose
	// type-local identifier is localID.
	Encode(typeName, localID string) (string, error)

	// Decode recovers the type name and type-local identifier from a global
	// identifier.
	Decode(globalID string) (typeName, localID string, err error)
}

// Base64GlobalIDCodec is the conventional Relay codec: base64 of
// "TypeName:localID".
//
// It is obfuscation, not secrecy — anyone can decode it. Supply a different
// GlobalIDCodec if identifiers must not be guessable.
type Base64GlobalIDCodec struct{}

func (Base64GlobalIDCodec) Encode(typeName, localID string) (string, error) {
	if strings.Contains(typeName, ":") {
		return "", fmt.Errorf("type name %q may not contain a colon", typeName)
	}
	return base64.StdEncoding.EncodeToString([]byte(typeName + ":" + localID)), nil
}

func (Base64GlobalIDCodec) Decode(globalID string) (string, string, error) {
	decoded, err := base64.StdEncoding.DecodeString(globalID)
	if err != nil {
		return "", "", fmt.Errorf("malformed global id")
	}
	// Split on the first colon only, so a local identifier may contain colons.
	typeName, localID, found := strings.Cut(string(decoded), ":")
	if !found || typeName == "" {
		return "", "", fmt.Errorf("malformed global id")
	}
	return typeName, localID, nil
}

var _ GlobalIDCodec = Base64GlobalIDCodec{}

// SetGlobalIDCodec replaces the codec used for Relay global identifiers. It
// must be called before the schema is built.
func (s *Schema) SetGlobalIDCodec(codec GlobalIDCodec) {
	s.globalIDCodec = codec
}

// NodeRef carries a value that is being returned through the Node interface,
// tagged with the name of its concrete type.
//
// A field declared to return *NodeRef has the GraphQL type Node.
type NodeRef struct {
	typeName string
	value    interface{}
}

// NodeOf tags a value with the GraphQL type name to resolve it as. typeName
// must be a type registered with (*Object).Node.
func NodeOf(typeName string, value interface{}) *NodeRef {
	if value == nil {
		return nil
	}
	return &NodeRef{typeName: typeName, value: value}
}

var nodeRefType = reflect.TypeOf(NodeRef{})

// nodeRegistration records how one object type participates in the Node
// interface.
type nodeRegistration struct {
	// localID produces a value's type-local identifier.
	localID interface{}
	// fetch loads a value from its type-local identifier.
	fetch interface{}
}

// Node declares that this object type implements the Relay Node interface.
//
// localID reports a value's type-local identifier and must have one of the
// shapes
//
//	func(*T) string
//	func(*T) (string, error)
//	func(context.Context, *T) (string, error)
//
// (a non-pointer *T is accepted too). fetch loads a value back from that
// identifier, and must be
//
//	func(string) (*T, error)
//	func(context.Context, string) (*T, error)
//
// Node defines the type's `id` field as its *global* identifier, replacing any
// `id` the Go struct would otherwise expose. That is what Relay's store keys
// off, and the type-local identifier stays available through whatever other
// field the type chooses to expose it on.
func (o *Object) Node(localID interface{}, fetch interface{}) *Object {
	o.node = &nodeRegistration{localID: localID, fetch: fetch}
	return o
}

// nodeType is one type participating in the Node interface, after its
// registration has been checked.
type nodeType struct {
	name    string
	goType  reflect.Type // the struct type, not a pointer
	localID func(ctx context.Context, source interface{}) (string, error)
	fetch   func(ctx context.Context, localID string) (interface{}, error)
}

var (
	contextInterface = reflect.TypeOf((*context.Context)(nil)).Elem()
	errorInterface   = reflect.TypeOf((*error)(nil)).Elem()
	stringType       = reflect.TypeOf("")
)

// buildNodeTypes validates every Node registration and returns them by type
// name.
func (s *Schema) buildNodeTypes() (map[string]*nodeType, error) {
	types := make(map[string]*nodeType)

	for name, object := range s.objects {
		if object.node == nil {
			continue
		}

		goType := reflect.TypeOf(object.Type)
		if goType.Kind() != reflect.Struct {
			return nil, fmt.Errorf("node type %s must be a struct", name)
		}

		localID, err := makeLocalIDFunc(name, goType, object.node.localID)
		if err != nil {
			return nil, err
		}
		fetch, err := makeFetchFunc(name, goType, object.node.fetch)
		if err != nil {
			return nil, err
		}

		types[name] = &nodeType{name: name, goType: goType, localID: localID, fetch: fetch}
	}

	return types, nil
}

// makeLocalIDFunc adapts a user-supplied local-identifier function to a uniform
// signature.
func makeLocalIDFunc(name string, goType reflect.Type, fn interface{}) (func(context.Context, interface{}) (string, error), error) {
	const shape = "func(*T) string, func(*T) (string, error) or func(context.Context, *T) (string, error)"

	value := reflect.ValueOf(fn)
	if !value.IsValid() || value.Kind() != reflect.Func {
		return nil, fmt.Errorf("node type %s: local id must be a function; expected %s", name, shape)
	}
	typ := value.Type()

	takesContext := typ.NumIn() == 2 && typ.In(0) == contextInterface
	if typ.NumIn() != 1 && !takesContext {
		return nil, fmt.Errorf("node type %s: local id function must take %s", name, shape)
	}

	sourceIndex := 0
	if takesContext {
		sourceIndex = 1
	}
	source := typ.In(sourceIndex)
	if source != goType && source != reflect.PointerTo(goType) {
		return nil, fmt.Errorf("node type %s: local id function must take %s or *%s, not %s",
			name, goType, goType, source)
	}
	wantsPointer := source.Kind() == reflect.Ptr

	returnsError := typ.NumOut() == 2 && typ.Out(1) == errorInterface
	if typ.NumOut() == 0 || typ.Out(0) != stringType || (typ.NumOut() != 1 && !returnsError) {
		return nil, fmt.Errorf("node type %s: local id function must return %s", name, shape)
	}

	return func(ctx context.Context, src interface{}) (string, error) {
		sourceValue := reflect.ValueOf(src)
		if wantsPointer && sourceValue.Kind() != reflect.Ptr {
			addressable := reflect.New(goType)
			addressable.Elem().Set(sourceValue)
			sourceValue = addressable
		} else if !wantsPointer && sourceValue.Kind() == reflect.Ptr {
			if sourceValue.IsNil() {
				return "", fmt.Errorf("node type %s: cannot take the id of a nil value", name)
			}
			sourceValue = sourceValue.Elem()
		}

		args := []reflect.Value{sourceValue}
		if takesContext {
			args = []reflect.Value{reflect.ValueOf(ctx), sourceValue}
		}

		out := value.Call(args)
		if returnsError && !out[1].IsNil() {
			return "", out[1].Interface().(error)
		}
		return out[0].String(), nil
	}, nil
}

// makeFetchFunc adapts a user-supplied by-identifier fetcher to a uniform
// signature.
func makeFetchFunc(name string, goType reflect.Type, fn interface{}) (func(context.Context, string) (interface{}, error), error) {
	const shape = "func(context.Context, string) (*T, error) or func(string) (*T, error)"

	value := reflect.ValueOf(fn)
	if !value.IsValid() || value.Kind() != reflect.Func {
		return nil, fmt.Errorf("node type %s: fetcher must be a function; expected %s", name, shape)
	}
	typ := value.Type()

	takesContext := typ.NumIn() == 2 && typ.In(0) == contextInterface
	if (typ.NumIn() != 1 || typ.In(0) != stringType) && !takesContext {
		return nil, fmt.Errorf("node type %s: fetcher must take %s", name, shape)
	}
	if takesContext && typ.In(1) != stringType {
		return nil, fmt.Errorf("node type %s: fetcher must take %s", name, shape)
	}

	if typ.NumOut() != 2 || typ.Out(1) != errorInterface {
		return nil, fmt.Errorf("node type %s: fetcher must return %s", name, shape)
	}
	result := typ.Out(0)
	if result != goType && result != reflect.PointerTo(goType) {
		return nil, fmt.Errorf("node type %s: fetcher must return %s or *%s, not %s",
			name, goType, goType, result)
	}

	return func(ctx context.Context, localID string) (interface{}, error) {
		args := []reflect.Value{reflect.ValueOf(localID)}
		if takesContext {
			args = []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(localID)}
		}

		out := value.Call(args)
		if !out[1].IsNil() {
			return nil, out[1].Interface().(error)
		}
		if out[0].Kind() == reflect.Ptr && out[0].IsNil() {
			return nil, nil
		}
		return out[0].Interface(), nil
	}, nil
}

// registerNodeIDFields defines the generated `id` field on every node type.
//
// The field is built with reflect.MakeFunc because its source parameter type is
// only known at run time; it then goes through the ordinary FieldFunc path, so
// it behaves exactly like a hand-written field.
func (s *Schema) registerNodeIDFields(nodeTypes map[string]*nodeType) {
	for name, node := range nodeTypes {
		object := s.objects[name]
		codec := s.globalIDCodec

		signature := reflect.FuncOf(
			[]reflect.Type{contextInterface, reflect.PointerTo(node.goType)},
			[]reflect.Type{reflect.TypeOf(ID{}), errorInterface},
			false,
		)

		resolve := reflect.MakeFunc(signature, func(args []reflect.Value) []reflect.Value {
			ctx, _ := args[0].Interface().(context.Context)
			source := args[1].Interface()

			globalID, err := encodeGlobalID(ctx, codec, node, source)
			if err != nil {
				return []reflect.Value{
					reflect.ValueOf(ID{}),
					reflect.ValueOf(&err).Elem(),
				}
			}
			return []reflect.Value{
				reflect.ValueOf(ID{Value: globalID}),
				reflect.Zero(errorInterface),
			}
		})

		object.FieldFunc("id", resolve.Interface())
	}
}

func encodeGlobalID(ctx context.Context, codec GlobalIDCodec, node *nodeType, source interface{}) (string, error) {
	localID, err := node.localID(ctx, source)
	if err != nil {
		return "", err
	}
	return codec.Encode(node.name, localID)
}

// buildNodeInterface assembles the Node interface from the built object types.
func buildNodeInterface(objects map[string]*graphql.Object) (*graphql.Interface, error) {
	iface := &graphql.Interface{
		Name:          NodeTypeName,
		Description:   NodeDescription,
		Fields:        make(map[string]*graphql.Field),
		PossibleTypes: objects,
	}

	names := make([]string, 0, len(objects))
	for name := range objects {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		object := objects[name]

		idField, ok := object.Fields["id"]
		if !ok {
			return nil, fmt.Errorf("node type %s has no id field", name)
		}
		if iface.Fields["id"] == nil {
			iface.Fields["id"] = idField
		}

		if object.Interfaces == nil {
			object.Interfaces = make(map[string]*graphql.Interface)
		}
		object.Interfaces[NodeTypeName] = iface
	}

	iface.TypeResolver = func(source interface{}) (string, interface{}, error) {
		ref, ok := source.(*NodeRef)
		if !ok || ref == nil {
			return "", nil, nil
		}
		return ref.typeName, ref.value, nil
	}

	return iface, nil
}

// nodeRootFields builds the `node` and `nodes` root fields.
//
// They are constructed directly rather than through FieldFunc because their
// result types are interfaces, and because `nodes` needs nullable list entries
// so an unknown identifier yields null rather than failing the whole request —
// which the reflection path, which forces list entries non-null, cannot express.
func nodeRootFields(iface *graphql.Interface, codec GlobalIDCodec, nodeTypes map[string]*nodeType) map[string]*graphql.Field {
	idScalar := newScalar(ScalarID)

	resolveOne := func(ctx context.Context, globalID string) (*NodeRef, error) {
		typeName, localID, err := codec.Decode(globalID)
		if err != nil {
			return nil, graphql.NewClientError("%s", err.Error())
		}

		node, ok := nodeTypes[typeName]
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
		return NodeOf(typeName, value), nil
	}

	return map[string]*graphql.Field{
		"node": {
			Description:     "Looks up a single object by its global identifier.",
			Type:            iface,
			Args:            map[string]graphql.Type{"id": &graphql.NonNull{Type: idScalar}},
			ArgDescriptions: map[string]string{"id": "The object's global identifier."},
			ParseArguments:  parseNodeArgs,
			Expensive:       true,
			Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
				ref, err := resolveOne(ctx, args.(nodeArgs).id)
				if err != nil {
					return nil, err
				}
				if ref == nil {
					// A typed nil would be reported as a present value.
					return nil, nil
				}
				return ref, nil
			},
		},
		"nodes": {
			Description: "Looks up several objects by their global identifiers, in the order given.",
			Type: &graphql.NonNull{Type: &graphql.List{
				Type: iface,
			}},
			Args:            map[string]graphql.Type{"ids": &graphql.NonNull{Type: &graphql.List{Type: &graphql.NonNull{Type: idScalar}}}},
			ArgDescriptions: map[string]string{"ids": "The objects' global identifiers."},
			ParseArguments:  parseNodesArgs,
			Expensive:       true,
			Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
				ids := args.(nodesArgs).ids
				refs := make([]*NodeRef, 0, len(ids))
				for _, id := range ids {
					ref, err := resolveOne(ctx, id)
					if err != nil {
						return nil, err
					}
					refs = append(refs, ref)
				}
				return refs, nil
			},
		},
	}
}

type nodeArgs struct{ id string }
type nodesArgs struct{ ids []string }

func parseNodeArgs(value interface{}) (interface{}, error) {
	args, err := argsMap(value)
	if err != nil {
		return nil, err
	}
	id, err := requiredIDArg(args, "id")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownArgs(args, "id"); err != nil {
		return nil, err
	}
	return nodeArgs{id: id}, nil
}

func parseNodesArgs(value interface{}) (interface{}, error) {
	args, err := argsMap(value)
	if err != nil {
		return nil, err
	}

	raw, ok := args["ids"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("ids: required argument missing")
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("ids: expected a list of IDs")
	}

	ids := make([]string, 0, len(list))
	for i, item := range list {
		id, err := idArgValue(item)
		if err != nil {
			return nil, fmt.Errorf("ids[%d]: %w", i, err)
		}
		ids = append(ids, id)
	}

	if err := rejectUnknownArgs(args, "ids"); err != nil {
		return nil, err
	}
	return nodesArgs{ids: ids}, nil
}

func argsMap(value interface{}) (map[string]interface{}, error) {
	if value == nil {
		return map[string]interface{}{}, nil
	}
	args, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("expected arguments object")
	}
	return args, nil
}

func requiredIDArg(args map[string]interface{}, name string) (string, error) {
	raw, ok := args[name]
	if !ok || raw == nil {
		return "", fmt.Errorf("%s: required argument missing", name)
	}
	id, err := idArgValue(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return id, nil
}

// idArgValue reads an ID argument, which the specification allows to arrive as
// a string or as an integer.
func idArgValue(raw interface{}) (string, error) {
	switch raw := raw.(type) {
	case string:
		return raw, nil
	case float64:
		if raw != float64(int64(raw)) {
			return "", fmt.Errorf("not a valid ID")
		}
		return fmt.Sprintf("%d", int64(raw)), nil
	default:
		return "", fmt.Errorf("not a valid ID")
	}
}

func rejectUnknownArgs(args map[string]interface{}, allowed ...string) error {
	for name := range args {
		known := false
		for _, a := range allowed {
			if name == a {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown argument %q", name)
		}
	}
	return nil
}
