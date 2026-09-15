// Package relay adds Relay's conventions to a lightning schema: the Node
// interface, globally unique identifiers, and cursor connections.
//
// It is a plugin, with no privileged access to the core — everything it does,
// any other package can do. That is deliberate: it is the proof that the
// extension seam is real rather than asserted.
//
//	b := lightning.New(relay.Plugin())
//
//	task := lightning.Object[Task](b)
//	relay.Node(b, store.Task)                       // Task gains id: ID!
//	relay.Connection(b.Query(), "tasks", store.Tasks)
package relay

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
)

// NodeTypeName is the name of the Node interface.
const NodeTypeName = "Node"

// Identified is implemented by a type that knows its own type-local
// identifier.
//
// Implementing it is the shortest way to become a node: relay.Node then needs
// only a fetcher, because the type already says how to name one of its values.
type Identified interface {
	NodeID() string
}

// Codec turns a type name and a type-local identifier into the single opaque
// string a client sees, and back.
//
// Implement it to change the wire form — to sign identifiers, say. The default
// is base64 of "TypeName:localID", which is Relay's convention and is
// obfuscation rather than secrecy: anyone can decode it.
type Codec interface {
	Encode(typeName, localID string) (string, error)
	Decode(globalID string) (typeName, localID string, err error)
}

// Base64Codec is the conventional Relay codec.
type Base64Codec struct{}

func (Base64Codec) Encode(typeName, localID string) (string, error) {
	if strings.Contains(typeName, ":") {
		return "", fmt.Errorf("a type name may not contain a colon: %q", typeName)
	}
	return base64.StdEncoding.EncodeToString([]byte(typeName + ":" + localID)), nil
}

func (Base64Codec) Decode(globalID string) (string, string, error) {
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

var _ Codec = Base64Codec{}

// Relay is the plugin. Install it with lightning.New(relay.Plugin()).
type Relay struct {
	codec       Codec
	maxPageSize int32

	// nodes holds every registered node type, keyed by its Go type.
	nodes map[reflect.Type]*nodeType
	// byName indexes the same set by GraphQL type name, which is what a global
	// identifier carries.
	byName map[string]*nodeType

	// connectionTypes caches the generated Connection type per element type, so
	// two connections over the same type are one type rather than two that
	// happen to agree.
	connectionTypes map[string]graphql.Type

	b *lightning.Builder
}

// nodeType is one type participating in the Node interface.
type nodeType struct {
	goType  reflect.Type
	name    string
	localID func(ctx context.Context, value any) (string, error)
	fetch   func(ctx context.Context, localID string) (any, error)
}

// Option configures the plugin.
type Option func(*Relay)

// WithCodec replaces the global identifier format.
func WithCodec(codec Codec) Option {
	return func(r *Relay) { r.codec = codec }
}

// WithMaxPageSize caps how many edges a connection will return, however many a
// client asks for.
func WithMaxPageSize(n int32) Option {
	return func(r *Relay) { r.maxPageSize = n }
}

// Plugin returns the Relay plugin.
func Plugin(options ...Option) *Relay {
	r := &Relay{
		codec:       Base64Codec{},
		maxPageSize: 500,
		nodes:       map[reflect.Type]*nodeType{},
		byName:      map[string]*nodeType{},
	}
	for _, option := range options {
		option(r)
	}
	return r
}

// PluginName identifies the plugin in error messages.
func (r *Relay) PluginName() string { return "relay" }

// Install records the builder and registers GID as the ID scalar, so that an
// argument declared as a GID arrives decoded.
func (r *Relay) Install(b *lightning.Builder) error {
	r.b = b

	lightning.ScalarAs[GID](b, "ID",
		func(id GID) (any, error) { return r.codec.Encode(id.Type, id.Local) },
		func(value any) (GID, error) {
			text, err := asID(value)
			if err != nil {
				return GID{}, err
			}
			typeName, local, err := r.codec.Decode(text)
			if err != nil {
				return GID{}, graphql.NewClientError("%s", err.Error())
			}
			return GID{Type: typeName, Local: local}, nil
		})

	return nil
}

// Codec returns the global identifier codec in use, so an application can
// encode and decode identifiers the same way the schema does.
func (r *Relay) Codec() Codec { return r.codec }

// MaxPageSize returns the cap on connection page size.
func (r *Relay) MaxPageSize() int32 { return r.maxPageSize }

// find returns the plugin installed on a builder.
func find(b *lightning.Builder) (*Relay, error) {
	for _, p := range b.Plugins() {
		if r, ok := p.(*Relay); ok {
			return r, nil
		}
	}
	return nil, fmt.Errorf("the relay plugin is not installed; pass relay.Plugin() to lightning.New")
}

// Node registers T as a Relay node, using its NodeID method for the local
// identifier.
//
//	func (t *Task) NodeID() string { return t.Key }
//
//	relay.Node(b, store.Task)
//
// The type gains an id field carrying its global identifier, joins the Node
// interface, and becomes reachable through the node and nodes root fields.
//
// T is the pointer type — relay.Node(b, store.Task) infers *Task from the
// fetcher — which is what lets the Identified constraint be checked by the
// compiler: a type without a NodeID method does not satisfy it, and the error
// says so.
func Node[T Identified](b *lightning.Builder, fetch func(ctx context.Context, localID string) (T, error)) {
	NodeFunc(b, func(value T) string { return value.NodeID() }, fetch)
}

// NodeFunc registers T as a Relay node with an explicit local identifier
// function, for a type that has no NodeID method.
//
// As with Node, T is the pointer type and is inferred from the fetcher.
func NodeFunc[T any](b *lightning.Builder, localID func(T) string, fetch func(ctx context.Context, localID string) (T, error)) {
	r, err := find(b)
	if err != nil {
		b.Errorf("relay.Node: %s", err)
		return
	}
	if localID == nil || fetch == nil {
		b.Errorf("relay.Node: %s needs both a local id function and a fetcher", typeNameOf[T]())
		return
	}

	pointerType := reflect.TypeFor[T]()
	if pointerType.Kind() != reflect.Ptr {
		b.Errorf("relay.Node: %s is not a pointer type; a node is fetched as a pointer so that a missing one can be nil", typeNameOf[T]())
		return
	}
	goType := pointerType.Elem()

	if _, already := r.nodes[goType]; already {
		b.Errorf("relay.Node: %s is registered twice", typeNameOf[T]())
		return
	}

	r.nodes[goType] = &nodeType{
		goType: goType,
		localID: func(_ context.Context, value any) (string, error) {
			typed, ok := value.(T)
			if !ok {
				// The executor may hand back the value rather than a pointer to
				// it, which is how a struct field's value arrives.
				addressable := reflect.New(goType)
				rv := reflect.ValueOf(value)
				if rv.IsValid() && rv.Type() == goType {
					addressable.Elem().Set(rv)
					typed, ok = addressable.Interface().(T)
				}
				if !ok {
					return "", fmt.Errorf("expected a %s, got %T", typeNameOf[T](), value)
				}
			}
			if reflect.ValueOf(typed).IsNil() {
				return "", fmt.Errorf("cannot take the id of a nil %s", typeNameOf[T]())
			}
			return localID(typed), nil
		},
		fetch: func(ctx context.Context, id string) (any, error) {
			value, err := fetch(ctx, id)
			if err != nil {
				return nil, err
			}
			if reflect.ValueOf(value).IsNil() {
				// A typed nil would read as a present value.
				return nil, nil
			}
			return value, nil
		},
	}
}

func typeNameOf[T any]() string {
	t := reflect.TypeFor[T]()
	if t.Name() == "" {
		return t.String()
	}
	return t.String()
}

// BeforeBuild adds the id field to every node type, assembles the Node
// interface, and adds the node and nodes root fields.
//
// It runs after the application has declared everything, because until then the
// set of node types is not known.
func (r *Relay) BeforeBuild(b *lightning.Builder) error {
	if len(r.nodes) == 0 {
		return nil
	}

	// Resolve each registered Go type to its declared GraphQL type.
	for _, declared := range b.DeclaredTypes() {
		node, ok := r.nodes[declared.GoType()]
		if !ok {
			continue
		}
		if !declared.IsObject() {
			return fmt.Errorf("%s is registered as a node but is not an object type", declared.Name())
		}
		node.name = declared.Name()
		r.byName[node.name] = node

		declared.AddField("id", &graphql.Field{
			Type:           &graphql.NonNull{Type: idScalar()},
			Description:    "A globally unique identifier, which node(id:) resolves back to this object.",
			ParseArguments: noArguments,
			Resolve: func(ctx context.Context, source, _ any, _ *graphql.SelectionSet) (any, error) {
				localID, err := node.localID(ctx, source)
				if err != nil {
					return nil, err
				}
				return r.codec.Encode(node.name, localID)
			},
		})
	}

	for goType, node := range r.nodes {
		if node.name == "" {
			return fmt.Errorf("%s is registered as a node but was never declared; call lightning.Object for it", typeName(goType))
		}
	}

	r.addRootFields(b)
	r.addIDToAbstractTypes(b)
	return nil
}

// addIDToAbstractTypes gives an interface an id field when every one of its
// members is a node.
//
// An interface whose members all have a global identifier can promise one, and
// a client that has an Actor should be able to ask for its id without first
// narrowing to a concrete type. Doing it here rather than asking the author to
// declare it keeps the promise in step with the members: add a member that is
// not a node and the promise goes away.
func (r *Relay) addIDToAbstractTypes(b *lightning.Builder) {
	for _, declared := range b.DeclaredTypes() {
		if !declared.IsInterface() {
			continue
		}

		members := declared.Members()
		if len(members) == 0 {
			continue
		}

		allNodes := true
		for _, member := range members {
			if _, ok := r.nodes[member.GoType()]; !ok {
				allNodes = false
				break
			}
		}
		if !allNodes || declared.HasField("id") {
			continue
		}

		declared.AddField("id", &graphql.Field{
			Type:           &graphql.NonNull{Type: idScalar()},
			Description:    "A globally unique identifier, which node(id:) resolves back to this object.",
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
		})
	}
}

// AfterBuild links every node type into the Node interface.
//
// It happens here rather than in BeforeBuild because the interface's possible
// types must be the built objects, which do not exist until then.
func (r *Relay) AfterBuild(b *lightning.Builder, schema *graphql.Schema) error {
	if len(r.nodes) == 0 {
		return nil
	}

	iface := r.nodeInterface(schema)
	if iface == nil {
		return nil
	}

	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}
	sort.Strings(names)

	query, ok := schema.Query.(*graphql.Object)
	if !ok {
		return fmt.Errorf("the query root is not an object")
	}

	for _, name := range names {
		object := findObject(query, name)
		if object == nil {
			continue
		}
		iface.PossibleTypes[name] = object
		if object.Interfaces == nil {
			object.Interfaces = map[string]*graphql.Interface{}
		}
		object.Interfaces[NodeTypeName] = iface
	}

	if len(iface.PossibleTypes) == 0 {
		return fmt.Errorf("no node type is reachable from the query root; a node type must be reachable to appear in the schema")
	}
	return nil
}

// findObject walks the schema from the query root looking for an object by
// name. The runtime has no type registry of its own, so this is how a plugin
// reaches a built type.
func findObject(root *graphql.Object, name string) *graphql.Object {
	seen := map[graphql.Type]bool{}

	var walk func(graphql.Type) *graphql.Object
	walk = func(t graphql.Type) *graphql.Object {
		if t == nil || seen[t] {
			return nil
		}
		seen[t] = true

		switch typed := t.(type) {
		case *graphql.NonNull:
			return walk(typed.Type)
		case *graphql.List:
			return walk(typed.Type)
		case *graphql.Object:
			if typed.Name == name {
				return typed
			}
			for _, fieldName := range sortedNames(typed.Fields) {
				if found := walk(typed.Fields[fieldName].Type); found != nil {
					return found
				}
			}
		case *graphql.Interface:
			for _, possible := range typed.PossibleTypes {
				if found := walk(possible); found != nil {
					return found
				}
			}
		case *graphql.Union:
			for _, member := range typed.Types {
				if found := walk(member); found != nil {
					return found
				}
			}
		}
		return nil
	}

	return walk(root)
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func typeName(t reflect.Type) string {
	if t.Name() == "" {
		return t.String()
	}
	return t.String()
}

func idScalar() *graphql.Scalar {
	return &graphql.Scalar{Type: "ID"}
}

func noArguments(args any) (any, error) {
	if m, ok := args.(map[string]any); ok && len(m) > 0 {
		return nil, fmt.Errorf("no arguments expected")
	}
	return nil, nil
}
