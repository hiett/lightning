package lightning

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"
)

// This file reads the struct tags that carry a type's documentation.
//
// Everything a field needs to say about itself is said on the field:
//
//	type Task struct {
//	    lightning.Object `graphql:"Task" description:"A unit of work."`
//
//	    Key   string  `graphql:"-"`
//	    Title string  `description:"What needs doing."`
//	    Notes *string `description:"Free-form notes." deprecated:"Use comments."`
//	}
//
// The alternative — naming the field again in a trailing option — was an
// unchecked string matched against a name the library derived by reflection,
// and it documented nothing at all when the two disagreed.

// Meta is embedded in a struct to carry the GraphQL type's own name and
// description. It is optional: a type with no marker takes its Go name.
//
//	type Task struct {
//	    lightning.Meta `graphql:"Task" description:"A unit of work."`
//
//	    Title string `description:"What needs doing."`
//	}
//
// One marker serves output types, input types and argument structs, because
// what it carries — a name and a description — is the same for all of them.
type Meta struct{}

var metaMarkerType = reflect.TypeOf(Meta{})

// knownTagKeys are the struct tag keys this package reads. A key that looks
// like one of these but is not is reported rather than ignored, because a
// silently misspelled `describe:"..."` documents nothing.
var knownTagKeys = map[string]bool{
	"graphql":     true,
	"description": true,
	"deprecated":  true,
	"default":     true,
	"sortable":    true,
	"filterable":  true,
}

// likelyTagKeys are misspellings common enough to be worth naming.
var likelyTagKeys = map[string]string{
	"describe":    "description",
	"desc":        "description",
	"doc":         "description",
	"deprecate":   "deprecated",
	"deprecation": "deprecated",
	"name":        "graphql",
	"gql":         "graphql",
	"sort":        "sortable",
	"sortby":      "sortable",
	"filter":      "filterable",
	"filterable?": "filterable",
	"searchable":  "filterable",
}

// typeDocs is what a type's marker field says about it.
type typeDocs struct {
	name        string
	description string
	found       bool
}

// readTypeDocs finds an embedded Object or Input marker and reads its tag.
func readTypeDocs(goType reflect.Type) typeDocs {
	if goType.Kind() != reflect.Struct {
		return typeDocs{}
	}
	for i := 0; i < goType.NumField(); i++ {
		field := goType.Field(i)
		if !field.Anonymous {
			continue
		}
		if field.Type != metaMarkerType {
			continue
		}
		return typeDocs{
			name:        field.Tag.Get("graphql"),
			description: field.Tag.Get("description"),
			found:       true,
		}
	}
	return typeDocs{}
}

// isMarkerField reports whether a struct field is the embedded marker, which is
// a documentation carrier rather than a GraphQL field.
func isMarkerField(field reflect.StructField) bool {
	return field.Anonymous && field.Type == metaMarkerType
}

// fieldDocs is what a struct field's tags say about it.
type fieldDocs struct {
	name        string
	description string
	deprecated  string
	defaultText string
	hasDefault  bool
	skip        bool

	// sortable and filterable say that a paginated list may be ordered by this
	// field, or searched by its text. They live on the field because that is
	// where the answer is: whether a title can be searched is a fact about the
	// title, not about every list that happens to contain one.
	sortable   bool
	filterable bool
}

// readFieldDocs reads one struct field's tags.
func readFieldDocs(field reflect.StructField) (fieldDocs, error) {
	docs := fieldDocs{}

	if err := checkTagKeys(field); err != nil {
		return docs, err
	}

	name, hasName := field.Tag.Lookup("graphql")
	if hasName {
		// A `graphql:"-"` field is not part of the schema. The comma form is
		// accepted so that `graphql:"-,"` can name a field literally "-",
		// matching encoding/json.
		if name == "-" {
			docs.skip = true
			return docs, nil
		}
		if before, _, found := strings.Cut(name, ","); found {
			name = before
		}
	}
	if name == "" {
		name = fieldName(field.Name)
	}
	docs.name = name

	docs.description = field.Tag.Get("description")
	if reason, ok := field.Tag.Lookup("deprecated"); ok {
		if reason == "" {
			reason = DefaultDeprecationReason
		}
		docs.deprecated = reason
	}
	if text, ok := field.Tag.Lookup("default"); ok {
		docs.defaultText = text
		docs.hasDefault = true
	}

	var err error
	if docs.sortable, err = boolTag(field, "sortable"); err != nil {
		return docs, err
	}
	if docs.filterable, err = boolTag(field, "filterable"); err != nil {
		return docs, err
	}

	return docs, nil
}

// boolTag reads a tag whose whole content is a yes or a no.
//
// An empty value means yes, so `sortable:""` and `sortable:"true"` say the same
// thing; anything else is reported rather than guessed at, because a tag that
// silently means the opposite of what it reads is worse than no tag.
func boolTag(field reflect.StructField, key string) (bool, error) {
	text, ok := field.Tag.Lookup(key)
	if !ok {
		return false, nil
	}
	switch strings.ToLower(text) {
	case "", "true", "yes":
		return true, nil
	case "false", "no":
		return false, nil
	}
	return false, fmt.Errorf("field %s has %s:%q; it should be \"true\" or \"false\"", field.Name, key, text)
}

// checkTagKeys reports a struct tag key that was probably meant to be one this
// package reads.
func checkTagKeys(field reflect.StructField) error {
	for key := range tagKeys(string(field.Tag)) {
		if knownTagKeys[key] {
			continue
		}
		if intended, ok := likelyTagKeys[key]; ok {
			return fmt.Errorf("field %s has a %q tag; did you mean %q?", field.Name, key, intended)
		}
	}
	return nil
}

// tagKeys returns the keys present in a struct tag. It follows the convention
// reflect.StructTag documents: space-separated key:"value" pairs.
func tagKeys(tag string) map[string]bool {
	keys := map[string]bool{}
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}

		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			break
		}
		key := tag[:i]
		tag = tag[i+1:]

		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		keys[key] = true
		tag = tag[i+1:]
	}
	return keys
}

// DefaultDeprecationReason is reported when something is marked deprecated
// without saying why. It matches the specification's default for @deprecated.
const DefaultDeprecationReason = "No longer supported"

// fieldName converts a Go field or method name to its GraphQL spelling.
//
// Go writes acronyms in capitals — OwnerID, HTTPServer, URL — and GraphQL
// writes them as ordinary words: ownerId, httpServer, url. Each run of two or
// more capitals is therefore treated as one word and title-cased, and the
// result's first letter is lowered.
//
//	Title      -> title
//	OwnerID    -> ownerId
//	ID         -> id
//	HTTPServer -> httpServer
//	UserURL    -> userUrl
//
// Lowering only the first letter — which is what this library used to do —
// turned OwnerID into ownerID, which is neither Go's spelling nor GraphQL's,
// and was a standing trap for anyone naming a field after an identifier.
func fieldName(goName string) string {
	if goName == "" {
		return ""
	}

	runes := []rune(goName)
	var out []rune

	for i := 0; i < len(runes); {
		if !unicode.IsUpper(runes[i]) {
			out = append(out, runes[i])
			i++
			continue
		}

		// The run of capitals starting here.
		j := i
		for j < len(runes) && unicode.IsUpper(runes[j]) {
			j++
		}
		run := runes[i:j]

		if len(run) == 1 {
			out = append(out, run[0])
			i = j
			continue
		}

		// A run of capitals followed by lower case belongs to two words: the
		// last capital starts the next one. HTTPServer is HTTP + Server.
		if j < len(runes) {
			run = runes[i : j-1]
			j--
		}

		// Title-case the acronym: ID becomes Id, HTTP becomes Http.
		out = append(out, run[0])
		for _, r := range run[1:] {
			out = append(out, unicode.ToLower(r))
		}
		i = j
	}

	out[0] = unicode.ToLower(out[0])
	return string(out)
}
