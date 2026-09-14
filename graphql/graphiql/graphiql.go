// Package graphiql serves the GraphiQL in-browser IDE as a development tool.
//
// The page itself is embedded in the binary with go:embed; the GraphiQL
// assets are loaded from a CDN, so there is no JavaScript build step.
package graphiql

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed index.html
var indexHTML string

// DefaultEndpoint is the GraphQL endpoint GraphiQL talks to when none is given.
const DefaultEndpoint = "/graphql"

const endpointPlaceholder = "__LIGHTNING_GRAPHQL_ENDPOINT__"

// Handler serves GraphiQL pointed at DefaultEndpoint.
func Handler() http.Handler {
	return HandlerForEndpoint(DefaultEndpoint)
}

// HandlerForEndpoint serves GraphiQL pointed at the given GraphQL endpoint
// path, for example "/api/graphql".
func HandlerForEndpoint(endpoint string) http.Handler {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	page := []byte(strings.Replace(indexHTML, endpointPlaceholder, jsStringEscape(endpoint), 1))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			w.Write(page)
		}
	})
}

// jsStringEscape escapes the characters that could break out of the single
// quoted JavaScript string literal the endpoint is substituted into.
func jsStringEscape(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
		"\n", `\n`,
		"\r", `\r`,
		`<`, `<`,
		`>`, `>`,
		`&`, `&`,
	).Replace(s)
}
