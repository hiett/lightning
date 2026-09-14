package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/hiett/lightning/batch"
	"github.com/hiett/lightning/reactive"
)

func HTTPHandler(schema *Schema, middlewares ...MiddlewareFunc) http.Handler {
	return HTTPHandlerWithExecutor(schema, (NewExecutor(NewImmediateGoroutineScheduler())), middlewares...)
}

func HTTPHandlerWithExecutor(schema *Schema, executor ExecutorRunner, middlewares ...MiddlewareFunc) http.Handler {
	// The validator is built once, up front. If the schema cannot be expressed
	// as SDL the error is reported on every request rather than swallowed,
	// because a schema that cannot be printed cannot be consumed by a client
	// either.
	validator, err := NewValidator(schema)
	return &httpHandler{
		schema:         schema,
		middlewares:    middlewares,
		executor:       executor,
		validator:      validator,
		validatorError: err,
	}
}

type httpHandler struct {
	schema      *Schema
	middlewares []MiddlewareFunc
	executor    ExecutorRunner

	validator      *Validator
	validatorError error
}

type httpPostBody struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName"`
	Variables     map[string]interface{} `json:"variables"`
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// requestError writes a response for a failure that happened before
	// execution began. The specification says "data" must be absent in that
	// case, which is how a client tells a request error from a field error.
	requestError := func(err error) {
		writeJSON(w, NewRequestErrorResponse(err))
	}

	writeResponse := func(value interface{}, err error) {
		writeJSON(w, NewResponse(value, err))
	}

	if r.Method != "POST" {
		requestError(NewClientError("request must be a POST"))
		return
	}

	if r.Body == nil {
		requestError(NewClientError("request must include a query"))
		return
	}

	var params httpPostBody
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		requestError(NewClientError("%s", err.Error()))
		return
	}

	if h.validatorError != nil {
		requestError(h.validatorError)
		return
	}

	query, err := h.validator.Parse(params.Query, params.Variables, params.OperationName)
	if err != nil {
		requestError(err)
		return
	}

	schema := h.schema.Query
	switch query.Kind {
	case "mutation":
		schema = h.schema.Mutation
	case "subscription":
		requestError(NewClientError("subscriptions are not supported over HTTP; use the websocket endpoint"))
		return
	}

	if err := PrepareQuery(r.Context(), schema, query.SelectionSet); err != nil {
		requestError(err)
		return
	}

	var wg sync.WaitGroup
	e := h.executor

	wg.Add(1)
	runner := reactive.NewRerunner(r.Context(), func(ctx context.Context) (interface{}, error) {
		defer wg.Done()

		ctx = batch.WithBatching(ctx)

		var middlewares []MiddlewareFunc
		middlewares = append(middlewares, h.middlewares...)
		middlewares = append(middlewares, func(input *ComputationInput, next MiddlewareNextFunc) *ComputationOutput {
			output := next(input)
			output.Current, output.Error = e.Execute(input.Ctx, schema, nil, input.ParsedQuery)
			return output
		})

		output := RunMiddlewares(middlewares, &ComputationInput{
			Ctx:         ctx,
			ParsedQuery: query,
			Query:       params.Query,
			Variables:   params.Variables,
		})
		current, err := output.Current, output.Error

		if err != nil {
			if ErrorCause(err) == context.Canceled {
				return nil, err
			}

			writeResponse(nil, err)
			return nil, err
		}

		writeResponse(current, nil)
		return nil, nil
	}, DefaultMinRerunInterval, false)

	wg.Wait()
	runner.Stop()
}

// writeJSON serialises a response body, falling back to a plain HTTP error if
// the response itself cannot be marshalled.
func writeJSON(w http.ResponseWriter, response *Response) {
	responseJSON, err := json.Marshal(response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Write(responseJSON)
}
