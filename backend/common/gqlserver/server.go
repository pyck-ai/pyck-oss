// Package gqlserver constructs the GraphQL handler shared by all pyck
// services.
package gqlserver

import (
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/vektah/gqlparser/v2/ast"
)

// New builds the production GraphQL server: HTTP transports only — no
// websocket, since the platform has no GraphQL subscriptions and open
// websockets would stall graceful shutdown (http.Server.Shutdown does not
// close hijacked connections) — plus query cache, introspection, automatic
// persisted queries, and an error presenter that keeps database constraint
// names out of client errors.
func New(es graphql.ExecutableSchema) *handler.Server {
	srv := handler.New(es)
	srv.SetErrorPresenter(presentError)

	srv.AddTransport(transport.Options{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})
	srv.AddTransport(transport.MultipartForm{})

	srv.SetQueryCache(lru.New[*ast.QueryDocument](1000))

	srv.Use(extension.Introspection{})
	srv.Use(extension.AutomaticPersistedQuery{
		Cache: lru.New[string](100),
	})

	return srv
}
