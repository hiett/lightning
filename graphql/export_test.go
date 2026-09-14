package graphql

// PathErrorInit builds a pathError for tests outside this package. path is
// innermost-first, and each segment is a field alias (string) or a list index
// (int).
func PathErrorInit(inner error, path []interface{}) error {
	return &pathError{
		inner: inner,
		path:  path,
	}
}

type PathError = pathError
