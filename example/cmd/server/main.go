// Command server runs the example lightning server.
//
// It serves:
//
//	POST /graphql        ordinary GraphQL over HTTP
//	GET  /graphql/ws     subscriptions over graphql-transport-ws
//	GET  /graphql/live   live queries over lightning's diff-pushing protocol
//	GET  /              GraphiQL
//
// The data lives in memory and is seeded at start-up, so there is nothing to
// set up: go run ./cmd/server and open http://localhost:8080.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hiett/lightning/example/schema"
	"github.com/hiett/lightning/graphql"
	"github.com/hiett/lightning/graphql/graphiql"
	"github.com/hiett/lightning/invalidation"
)

func main() {
	addr := flag.String("addr", ":8080", "the address to listen on")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The invalidator is what turns a write into a re-run of every live query
	// that read the same data. MemorySource keeps events inside this process,
	// which is all a single-process server needs.
	invalidator := invalidation.New(invalidation.NewMemorySource())
	go func() {
		if err := invalidator.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("invalidation: %v", err)
		}
	}()

	store := schema.NewStore(invalidator)
	built := schema.Build(store)

	mux := http.NewServeMux()
	mux.Handle("/graphql", withCORS(graphql.HTTPHandler(built)))
	mux.Handle("/graphql/ws", withCORS(graphql.TransportWSHandler(built)))
	mux.Handle("/graphql/live", withCORS(graphql.Handler(built)))
	mux.Handle("/", graphiql.HandlerForEndpoint("/graphql"))

	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	log.Printf("listening on %s — GraphiQL at http://localhost%s", *addr, *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// withCORS lets the example Relay app, served by a development server on
// another port, talk to this one.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
