package introspection

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/hiett/lightning"
	"github.com/hiett/lightning/graphql"
)

type introspection struct {
	types        map[string]graphql.Type
	query        graphql.Type
	mutation     graphql.Type
	subscription graphql.Type
}

type DirectiveLocation string

const (
	QUERY               DirectiveLocation = "QUERY"
	MUTATION                              = "MUTATION"
	FIELD                                 = "FIELD"
	FRAGMENT_DEFINITION                   = "FRAGMENT_DEFINITION"
	FRAGMENT_SPREAD                       = "FRAGMENT_SPREAD"
	INLINE_FRAGMENT                       = "INLINE_FRAGMENT"
	SUBSCRIPTION                          = "SUBSCRIPTION"
	FIELD_DEFINITION                      = "FIELD_DEFINITION"
	ENUM_VALUE                            = "ENUM_VALUE"
)

type TypeKind string

const (
	SCALAR       TypeKind = "SCALAR"
	OBJECT                = "OBJECT"
	INTERFACE             = "INTERFACE"
	UNION                 = "UNION"
	ENUM                  = "ENUM"
	INPUT_OBJECT          = "INPUT_OBJECT"
	LIST                  = "LIST"
	NON_NULL              = "NON_NULL"
)

type InputValue struct {
	Name         string
	Description  string
	Type         Type
	DefaultValue *string
}

func (s *introspection) registerInputValue(b *lightning.Builder) {
	lightning.Object[InputValue](b).Name("__InputValue")
}

type EnumValue struct {
	Name              string
	Description       string
	IsDeprecated      bool
	DeprecationReason string
}

func (s *introspection) registerEnumValue(b *lightning.Builder) {
	lightning.Object[EnumValue](b).Name("__EnumValue")
}

type Directive struct {
	Name        string
	Description string
	Locations   []DirectiveLocation
	Args        []InputValue
}

func (s *introspection) registerDirective(b *lightning.Builder) {
	lightning.Object[Directive](b).Name("__Directive")
}

type Schema struct {
	Types            []Type
	QueryType        *Type
	MutationType     *Type
	SubscriptionType *Type
	Directives       []Directive
}

func (s *introspection) registerSchema(b *lightning.Builder) {
	lightning.Object[Schema](b).Name("__Schema")
}

type Type struct {
	Inner graphql.Type `graphql:"-"`
}

var IncludeDirective = Directive{
	Description: "Directs the executor to include this field or fragment only when the `if` argument is true.",
	Locations: []DirectiveLocation{
		FIELD,
		FRAGMENT_SPREAD,
		INLINE_FRAGMENT,
	},
	Name: "include",
	Args: []InputValue{
		InputValue{
			Name:        "if",
			Type:        Type{Inner: &graphql.NonNull{Type: &graphql.Scalar{Type: "Boolean"}}},
			Description: "Included when true.",
		},
	},
}

var SkipDirective = Directive{
	Description: "Directs the executor to skip this field or fragment only when the `if` argument is true.",
	Locations: []DirectiveLocation{
		FIELD,
		FRAGMENT_SPREAD,
		INLINE_FRAGMENT,
	},
	Name: "skip",
	Args: []InputValue{
		InputValue{
			Name:        "if",
			Type:        Type{Inner: &graphql.NonNull{Type: &graphql.Scalar{Type: "Boolean"}}},
			Description: "Skipped when true.",
		},
	},
}

var DeprecatedDirective = Directive{
	Description: "Marks an element of a GraphQL schema as no longer supported.",
	Locations: []DirectiveLocation{
		FIELD_DEFINITION,
		ENUM_VALUE,
	},
	Name: "deprecated",
	Args: []InputValue{
		{
			Name:        "reason",
			Type:        Type{Inner: &graphql.Scalar{Type: "String"}},
			Description: "Explains why this element was deprecated.",
		},
	},
}

func (s *introspection) registerType(b *lightning.Builder) {
	object := lightning.Object[Type](b).Name("__Type")
	object.Attr("kind", func(t *Type) TypeKind {
		switch t.Inner.(type) {
		case *graphql.Object:
			return OBJECT
		case *graphql.Interface:
			return INTERFACE
		case *graphql.Union:
			return UNION
		case *graphql.Scalar:
			return SCALAR
		case *graphql.Enum:
			return ENUM
		case *graphql.List:
			return LIST
		case *graphql.InputObject:
			return INPUT_OBJECT
		case *graphql.NonNull:
			return NON_NULL
		default:
			return ""
		}
	})

	object.Attr("name", func(t *Type) *string {
		switch t := t.Inner.(type) {
		case *graphql.Object:
			return &t.Name
		case *graphql.Interface:
			return &t.Name
		case *graphql.Union:
			return &t.Name
		case *graphql.Scalar:
			return &t.Type
		case *graphql.Enum:
			return &t.Type
		case *graphql.InputObject:
			return &t.Name
		default:
			return nil
		}
	})

	object.Attr("description", func(t *Type) string {
		switch t := t.Inner.(type) {
		case *graphql.Object:
			return t.Description
		case *graphql.Interface:
			return t.Description
		case *graphql.Union:
			return t.Description
		case *graphql.Scalar:
			return t.Description
		case *graphql.Enum:
			return t.Description
		case *graphql.InputObject:
			return t.Description
		default:
			return ""
		}
	})

	object.Attr("interfaces", func(t *Type) []Type {
		object, ok := t.Inner.(*graphql.Object)
		if !ok {
			return nil
		}
		types := make([]Type, 0, len(object.Interfaces))
		for _, iface := range object.Interfaces {
			types = append(types, Type{Inner: iface})
		}
		sortTypes(types)
		return types
	})

	object.Attr("possibleTypes", func(t *Type) []Type {
		var objects map[string]*graphql.Object
		switch t := t.Inner.(type) {
		case *graphql.Union:
			objects = t.Types
		case *graphql.Interface:
			objects = t.PossibleTypes
		default:
			return nil
		}

		types := make([]Type, 0, len(objects))
		for _, typ := range objects {
			types = append(types, Type{Inner: typ})
		}
		sortTypes(types)
		return types
	})

	object.Attr("inputFields", func(t *Type) []InputValue {
		var fields []InputValue

		switch t := t.Inner.(type) {
		case *graphql.InputObject:
			for name, f := range t.InputFields {
				fields = append(fields, InputValue{
					Name:        name,
					Description: t.FieldDescriptions[name],
					Type:        Type{Inner: f},
				})
			}
		}

		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		return fields
	})

	object.FieldArgs("fields", func(_ context.Context, t *Type, args includeDeprecatedArgs) ([]field, error) {
		var source map[string]*graphql.Field
		switch t := t.Inner.(type) {
		case *graphql.Object:
			source = t.Fields
		case *graphql.Interface:
			source = t.Fields
		default:
			return nil, nil
		}

		includeDeprecated := args.IncludeDeprecated != nil && *args.IncludeDeprecated

		var fields []field
		for name, f := range source {
			if f.DeprecationReason != "" && !includeDeprecated {
				continue
			}

			var args []InputValue
			for argName, a := range f.Args {
				args = append(args, InputValue{
					Name:        argName,
					Description: f.ArgDescriptions[argName],
					Type:        Type{Inner: a},
				})
			}
			sort.Slice(args, func(i, j int) bool { return args[i].Name < args[j].Name })

			fields = append(fields, field{
				Name:              name,
				Description:       f.Description,
				Type:              Type{Inner: f.Type},
				Args:              args,
				IsDeprecated:      f.DeprecationReason != "",
				DeprecationReason: f.DeprecationReason,
			})
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })

		return fields, nil
	})

	object.Attr("ofType", func(t *Type) *Type {
		switch t := t.Inner.(type) {
		case *graphql.List:
			return &Type{Inner: t.Type}
		case *graphql.NonNull:
			return &Type{Inner: t.Type}
		default:
			return nil
		}
	})

	object.FieldArgs("enumValues", func(_ context.Context, t *Type, args includeDeprecatedArgs) ([]EnumValue, error) {
		enum, ok := t.Inner.(*graphql.Enum)
		if !ok {
			return nil, nil
		}

		includeDeprecated := args.IncludeDeprecated != nil && *args.IncludeDeprecated

		var enumVals []EnumValue
		for _, name := range enum.Values {
			reason := enum.DeprecationReasons[name]
			if reason != "" && !includeDeprecated {
				continue
			}
			enumVals = append(enumVals, EnumValue{
				Name:              name,
				Description:       enum.Descriptions[name],
				IsDeprecated:      reason != "",
				DeprecationReason: reason,
			})
		}
		sort.Slice(enumVals, func(i, j int) bool { return enumVals[i].Name < enumVals[j].Name })
		return enumVals, nil
	})
}

// sortTypes orders a type list by name so introspection output is stable.
func sortTypes(types []Type) {
	sort.Slice(types, func(i, j int) bool { return types[i].Inner.String() < types[j].Inner.String() })
}

// includeDeprecatedArgs is the argument both __Type.fields and
// __Type.enumValues take, spelled once.
type includeDeprecatedArgs struct {
	IncludeDeprecated *bool
}

type field struct {
	Name              string
	Description       string
	Args              []InputValue
	Type              Type
	IsDeprecated      bool
	DeprecationReason string
}

func (s *introspection) registerField(b *lightning.Builder) {
	lightning.Object[field](b).Name("__Field")
}

func collectTypes(typ graphql.Type, types map[string]graphql.Type) {
	switch typ := typ.(type) {
	case *graphql.Object:
		if _, ok := types[typ.Name]; ok {
			return
		}
		types[typ.Name] = typ

		for _, field := range typ.Fields {
			collectTypes(field.Type, types)

			for _, arg := range field.Args {
				collectTypes(arg, types)
			}
		}

	case *graphql.Interface:
		if _, ok := types[typ.Name]; ok {
			return
		}
		types[typ.Name] = typ

		for _, field := range typ.Fields {
			collectTypes(field.Type, types)

			for _, arg := range field.Args {
				collectTypes(arg, types)
			}
		}
		for _, possible := range typ.PossibleTypes {
			collectTypes(possible, types)
		}

	case *graphql.Union:
		if _, ok := types[typ.Name]; ok {
			return
		}
		types[typ.Name] = typ
		for _, graphqlTyp := range typ.Types {
			collectTypes(graphqlTyp, types)
		}

	case *graphql.List:
		collectTypes(typ.Type, types)

	case *graphql.Scalar:
		if _, ok := types[typ.Type]; ok {
			return
		}
		types[typ.Type] = typ

	case *graphql.Enum:
		if _, ok := types[typ.Type]; ok {
			return
		}
		types[typ.Type] = typ

	case *graphql.InputObject:
		if _, ok := types[typ.Name]; ok {
			return
		}
		types[typ.Name] = typ

		for _, field := range typ.InputFields {
			collectTypes(field, types)
		}

	case *graphql.NonNull:
		collectTypes(typ.Type, types)
	}
}

func (s *introspection) registerQuery(b *lightning.Builder) {
	object := b.Query()

	object.Attr("__schema", func(_ *lightning.Root) *Schema {
		var types []Type

		for _, typ := range s.types {
			types = append(types, Type{Inner: typ})
		}
		sort.Slice(types, func(i, j int) bool { return types[i].Inner.String() < types[j].Inner.String() })

		schema := &Schema{
			Types:        types,
			QueryType:    &Type{Inner: s.query},
			MutationType: &Type{Inner: s.mutation},
			Directives: []Directive{
				IncludeDirective,
				SkipDirective,
				DeprecatedDirective,
			},
		}
		if s.subscription != nil {
			schema.SubscriptionType = &Type{Inner: s.subscription}
		}
		return schema
	})

	object.FieldArgs("__type", func(_ context.Context, _ *lightning.Root, args typeArgs) (*Type, error) {
		if typ, ok := s.types[args.Name]; ok {
			return &Type{Inner: typ}, nil
		}
		return nil, nil
	})
}

func (s *introspection) registerMutation(b *lightning.Builder) {}

// typeArgs names the type __type(name:) is asked about.
type typeArgs struct {
	Name string
}

func (s *introspection) schema() *graphql.Schema {
	b := lightning.New()
	s.registerDirective(b)
	s.registerEnumValue(b)
	s.registerField(b)
	s.registerInputValue(b)
	s.registerMutation(b)
	s.registerQuery(b)
	s.registerSchema(b)
	s.registerType(b)

	return b.MustBuild()
}

func BareIntrospectionSchema(schema *graphql.Schema) *graphql.Schema {
	types := make(map[string]graphql.Type)
	collectTypes(schema.Query, types)
	collectTypes(schema.Mutation, types)
	collectTypes(schema.Subscription, types)
	is := &introspection{
		types:        types,
		query:        schema.Query,
		mutation:     schema.Mutation,
		subscription: schema.Subscription,
	}
	return is.schema()
}

func AddIntrospectionToSchema(schema *graphql.Schema) {
	isSchema := BareIntrospectionSchema(schema)
	query := schema.Query.(*graphql.Object)

	isQuery := isSchema.Query.(*graphql.Object)
	for k, v := range query.Fields {
		isQuery.Fields[k] = v
	}

	schema.Query = isQuery
}

// ComputeSchemaJSON returns the result of executing a GraphQL introspection
// query against a built schema.
func ComputeSchemaJSON(schema *graphql.Schema) ([]byte, error) {
	AddIntrospectionToSchema(schema)
	return RunIntrospectionQuery(schema)
}

// RunIntrospectionQuery returns the result of executing a GraphQL introspection
// query.
func RunIntrospectionQuery(schema *graphql.Schema) ([]byte, error) {
	query, err := graphql.Parse(IntrospectionQuery, map[string]interface{}{})
	if err != nil {
		return nil, err
	}

	if err := graphql.PrepareQuery(context.Background(), schema.Query, query.SelectionSet); err != nil {
		return nil, err
	}

	executor := graphql.NewExecutor(graphql.NewImmediateGoroutineScheduler())
	value, err := executor.Execute(context.Background(), schema.Query, nil, query)
	if err != nil {
		return nil, err
	}

	return json.MarshalIndent(value, "", "  ")
}

func IntrospectionQueryTypeOrSelection(typeName, selectionName string) bool {
	if selectionName == "__schema" ||
		typeName == "__Schema" ||
		typeName == "__Directive" ||
		typeName == "__InputValue" ||
		typeName == "__Type" ||
		typeName == "__EnumValue" ||
		typeName == "__Field" {
		return true
	}

	return false
}
