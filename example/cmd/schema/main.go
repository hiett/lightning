// Command schema writes the example's schema.graphql.
//
// It is the build step a consuming project wires up so that the schema file
// its client tooling reads is generated from the Go schema rather than
// maintained by hand:
//
//	go run ./cmd/schema
//
// See the go:generate directive in example/generate.go.
package main

import (
	"flag"
	"log"

	"github.com/hiett/lightning/example/schema"
	"github.com/hiett/lightning/graphql"
)

func main() {
	out := flag.String("out", "schema.graphql", "where to write the schema")
	flag.Parse()

	if err := graphql.WriteSchemaFile(schema.MustBuild(), *out); err != nil {
		log.Fatalf("writing %s: %v", *out, err)
	}
	log.Printf("wrote %s", *out)
}
