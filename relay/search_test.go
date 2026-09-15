package relay

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSearchMatching pins how filterText is read: quoted runs are phrases,
// matching is case-insensitive, and an empty quoted term matches nothing.
func TestSearchMatching(t *testing.T) {
	testcases := []struct {
		String  string
		Query   string
		Matches bool
	}{
		{"hi, san francisco", `"san fran"`, true},
		{"hi, San Francisco", `"san fran"`, true},
		{"hi, San Francisco", `"SAN FRAN"`, true},
		{"hi, san francisco", `"san fran" and`, true},
		{"hi, sandy francisco", `"san fran"`, false},
		{"hi, sandy francisco", `"san fran" and`, true},
		{"hi, sandy francisco", ``, true},
		{"hi, sandy francisco", `""`, false},
	}

	for _, tc := range testcases {
		searchTokens := tokenize(tc.Query)
		assert.Equal(t,
			tc.Matches,
			matches(tc.String, searchTokens),
			"expected Match(`%s`, `%s`) to be %v", tc.String, tc.Query, tc.Matches,
		)
	}
}
