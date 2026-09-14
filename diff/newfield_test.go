package diff_test

import (
	"encoding/json"
	"testing"

	"github.com/hiett/lightning/diff"
	"github.com/hiett/lightning/merge"
	"github.com/stretchr/testify/require"
)

// TestNewFieldRoundTrips checks that a field which did not exist in the
// previous result survives the diff.
//
// A new field's value used to be written into the diff raw, which made it
// indistinguishable from a diff node: a new field holding [] was read as a
// deletion, one holding [v] was unwrapped to v, and one holding an object was
// recursed into as though it were a diff. Since a live query re-executes
// whenever its data changes, and a field appearing for the first time is
// completely ordinary — a connection's first edge, a nullable field becoming
// non-null — this corrupted payloads routinely.
func TestNewFieldRoundTrips(t *testing.T) {
	for _, tt := range []struct {
		name     string
		old, new string
	}{
		{"new empty list", `{"a":1}`, `{"a":1,"b":[]}`},
		{"new one-element list", `{"a":1}`, `{"a":1,"b":["x"]}`},
		{"new longer list", `{"a":1}`, `{"a":1,"b":["x","y"]}`},
		{"new list of objects", `{"a":1}`, `{"a":1,"b":[{"c":2}]}`},
		{"new nested list", `{"o":{}}`, `{"o":{"t":["x"]}}`},
		{"new object", `{"a":1}`, `{"a":1,"b":{"c":2}}`},
		{"new scalar", `{"a":1}`, `{"a":1,"b":"x"}`},
		{"new null", `{"a":1}`, `{"a":1,"b":null}`},
		{"first field of all", `{}`, `{"b":[1,2]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var old, want interface{}
			require.NoError(t, json.Unmarshal([]byte(tt.old), &old))
			require.NoError(t, json.Unmarshal([]byte(tt.new), &want))

			d := diff.Diff(old, want)

			// The diff has to survive the wire, so round-trip it through JSON
			// exactly as a client would receive it.
			encoded, err := json.Marshal(d)
			require.NoError(t, err)
			var decoded interface{}
			require.NoError(t, json.Unmarshal(encoded, &decoded))

			merged, err := merge.Merge(old, decoded)
			require.NoError(t, err, "diff was %s", encoded)

			gotJSON, err := json.Marshal(merged)
			require.NoError(t, err)
			require.JSONEq(t, tt.new, string(gotJSON), "diff was %s", encoded)
		})
	}
}
