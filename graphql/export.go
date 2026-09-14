package graphql

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// This file exports a schema as a file on disk, so that generating
// schema.graphql can be a build step in a consuming project.

// WriteSchema writes a schema as SDL to w.
func WriteSchema(w io.Writer, schema *Schema) error {
	sdl, err := PrintSchema(schema)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, sdl); err != nil {
		return err
	}
	return nil
}

// WriteSchemaFile writes a schema as SDL to path, creating any missing parent
// directories.
//
// It is meant to be called from a tiny main package in the consuming project,
// wired up with go:generate, so that the schema file a client's tooling reads
// is regenerated from the Go schema rather than maintained by hand:
//
//	//go:generate go run ./cmd/schema
//
//	func main() {
//	    if err := graphql.WriteSchemaFile(myschema.Build(), "schema.graphql"); err != nil {
//	        log.Fatal(err)
//	    }
//	}
//
// The file is written atomically — rendered to a temporary file in the same
// directory and renamed — so a build step that fails leaves the previous schema
// in place rather than a truncated one.
func WriteSchemaFile(schema *Schema, path string) error {
	sdl, err := PrintSchema(schema)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	temp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if _, err := temp.WriteString(sdl); err != nil {
		temp.Close()
		return fmt.Errorf("writing %s: %w", tempName, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", tempName, err)
	}
	if err := os.Chmod(tempName, 0o644); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", tempName, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tempName, path, err)
	}
	return nil
}
