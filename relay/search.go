package relay

import (
	"regexp"
	"strings"
)

// This file is how filterText is read and matched.
//
// It lives here rather than in the core because searching text is a property of
// this plugin's connections, not of the schema builder: another plugin could
// define a different search and the core would be none the wiser.

// searchTerms splits a search string into the terms a value must match.
//
// A quoted run is one term, so "san fran" matches a phrase and san fran matches
// either word.
var searchTerms = regexp.MustCompile(`(?:([^\s"]+)|"([^"]*)"?)+`)

// tokenize reads a client's filterText into the terms to look for.
func tokenize(query string) []string {
	if query == "" {
		return nil
	}

	matches := searchTerms.FindAllStringSubmatch(query, -1)
	tokens := make([]string, 0, len(matches))
	for _, match := range matches {
		// Empty quotes match nothing rather than everything, so they are kept
		// as an empty term rather than dropped.
		if match[1] == "" && match[2] == "" {
			tokens = append(tokens, "")
			continue
		}
		text := match[1]
		if text == "" {
			text = match[2]
		}
		tokens = append(tokens, text)
	}
	return tokens
}

// matches reports whether a value's text satisfies the search.
//
// Any term matching is enough, and matching is case-insensitive on a substring:
// this is the search a person types into a box, not a query language.
func matches(text string, tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	lowered := strings.ToLower(text)
	for _, token := range tokens {
		if token != "" && strings.Contains(lowered, strings.ToLower(token)) {
			return true
		}
	}
	return false
}
