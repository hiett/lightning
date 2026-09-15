package relay

import (
	"fmt"
	"reflect"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
)

// This file is the typed global identifier.
//
// A GID accepts any global id, which is right for node(id:) and wrong almost
// everywhere else: a mutation that takes a task's id has no use for a user's,
// and being handed one is a client mistake that should be reported as one.
//
//	type SetDoneArgs struct {
//	    ID   relay.ID[Task]
//	    Done bool
//	}
//
// ID[Task] is an ID on the wire and a decoded local identifier in Go, and an
// identifier naming anything but a Task is refused while the arguments are
// parsed — before the resolver runs, and with a message saying what was
// expected. That is the localID helper every project writes by hand, with the
// check the hand-written one usually forgets.

// ID is a global identifier that must name a T.
type ID[T any] struct {
	// Local is the type-local identifier the global one decoded to.
	Local string
}

// String renders the local identifier, which is what a resolver is holding.
func (i ID[T]) String() string { return i.Local }

// nodeGoType reports the type an identifier must name. It is what lets the
// plugin recognise an ID[T] without knowing T.
func (i ID[T]) nodeGoType() reflect.Type {
	goType := reflect.TypeFor[T]()
	for goType.Kind() == reflect.Ptr {
		goType = goType.Elem()
	}
	return goType
}

// setLocal fills in the decoded identifier.
func (i *ID[T]) setLocal(local string) { i.Local = local }

// typedID is what every ID[T] has in common.
type typedID interface {
	nodeGoType() reflect.Type
	setLocal(string)
}

var typedIDType = reflect.TypeOf((*typedID)(nil)).Elem()

// claimTypedIDs tells the builder how an ID[T] travels.
//
// It is registered once, on Install, and consulted for every Go type the
// builder does not otherwise recognise — which is the only way to reach a
// generic type, since nothing can enumerate the instantiations an application
// will use.
func (r *Relay) claimTypedIDs(b *lightning.Builder) {
	b.ScalarShapes(func(goType reflect.Type) *lightning.ScalarBinding {
		if goType.Kind() != reflect.Struct || !reflect.PointerTo(goType).Implements(typedIDType) {
			return nil
		}

		// A fresh value, only to ask it which type it is about.
		nodeGoType := reflect.New(goType).Interface().(typedID).nodeGoType()

		return &lightning.ScalarBinding{
			Name: "ID",
			Encode: func(value any) (any, error) {
				typed, ok := asTypedID(value)
				if !ok {
					return nil, fmt.Errorf("cannot serialise %T as an ID", value)
				}
				name, err := r.nodeName(nodeGoType)
				if err != nil {
					return nil, err
				}
				return r.codec.Encode(name, typed.(interface{ String() string }).String())
			},
			Decode: func(value any) (any, error) {
				want, err := r.nodeName(nodeGoType)
				if err != nil {
					return nil, err
				}

				text, err := asID(value)
				if err != nil {
					return nil, err
				}
				typeName, local, err := r.codec.Decode(text)
				if err != nil {
					return nil, graphql.NewClientError("%s", err.Error())
				}
				if typeName != want {
					return nil, graphql.NewClientError("expected the global id of a %s, but this one names a %s", want, typeName)
				}

				out := reflect.New(goType)
				out.Interface().(typedID).setLocal(local)
				return out.Elem().Interface(), nil
			},
		}
	})
}

// asTypedID reaches the typedID behind a value, which arrives as the struct
// itself or as a pointer to it.
func asTypedID(value any) (any, bool) {
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return nil, false
	}
	if !reflect.PointerTo(rv.Type()).Implements(typedIDType) {
		return nil, false
	}
	return rv.Interface(), true
}

// nodeName gives the GraphQL name a Go type was registered as a node under.
func (r *Relay) nodeName(goType reflect.Type) (string, error) {
	node, ok := r.nodes[goType]
	if !ok {
		return "", fmt.Errorf("relay.ID[%s]: %s is not registered as a node; register it with relay.Node", typeName(goType), typeName(goType))
	}
	if node.name == "" {
		return "", fmt.Errorf("relay.ID[%s]: %s has not been declared; declare it with lightning.Object", typeName(goType), typeName(goType))
	}
	return node.name, nil
}
