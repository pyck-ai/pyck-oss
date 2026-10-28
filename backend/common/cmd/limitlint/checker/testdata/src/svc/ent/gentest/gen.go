// Package gentest is a stub of a real ent/gen package. The directory
// is deliberately not named "gen" so that the project-wide
// `task clean` step (which runs `find . -type d -name "gen" -exec
// rm -rf {} +`) does not wipe these fixtures. The analyzer's
// "must end in /ent/gen" suffix check is overridden in the test via
// Config.EntGenPackageSuffix.
package gentest

import "context"

type Client struct {
	Item  *ItemClient
	Event *EventClient
}

type ItemClient struct{}

func (*ItemClient) Query() *ItemQuery     { return &ItemQuery{} }
func (*ItemClient) Mutate() *ItemMutation { return &ItemMutation{} }

type ItemQuery struct{}

func (q *ItemQuery) All(ctx context.Context) ([]*Item, error) { return nil, nil }
func (q *ItemQuery) IDs(ctx context.Context) ([]int, error)   { return nil, nil }
func (q *ItemQuery) Limit(int) *ItemQuery                     { return q }
func (q *ItemQuery) Offset(int) *ItemQuery                    { return q }
func (q *ItemQuery) Where(...any) *ItemQuery                  { return q }
func (q *ItemQuery) Order(...any) *ItemQuery                  { return q }
func (q *ItemQuery) AllPages(ctx context.Context, pageSize int) ([]*Item, error) {
	return nil, nil
}
func (q *ItemQuery) PagedIDs(ctx context.Context, pageSize int) ([]int, error) {
	return nil, nil
}

// Select returns a projection that embeds the query, the way ent's
// generated *<Entity>Select does: .All/.IDs/.Limit/.AllPages are promoted
// from the query, and the scalar scanners are added on top.
func (q *ItemQuery) Select(...string) *ItemSelect { return &ItemSelect{ItemQuery: q} }

// GroupBy returns a projection that holds the query as a field, the way
// ent's generated *<Entity>GroupBy does: nothing is promoted, so a .Limit
// must come before .GroupBy in the chain.
func (q *ItemQuery) GroupBy(...string) *ItemGroupBy { return &ItemGroupBy{build: q} }

type ItemSelect struct{ *ItemQuery }

func (s *ItemSelect) Strings(ctx context.Context) ([]string, error)   { return nil, nil }
func (s *ItemSelect) Ints(ctx context.Context) ([]int, error)         { return nil, nil }
func (s *ItemSelect) Float64s(ctx context.Context) ([]float64, error) { return nil, nil }
func (s *ItemSelect) Bools(ctx context.Context) ([]bool, error)       { return nil, nil }
func (s *ItemSelect) Scan(ctx context.Context, v any) error           { return nil }

type ItemGroupBy struct{ build *ItemQuery }

func (g *ItemGroupBy) Strings(ctx context.Context) ([]string, error) { return nil, nil }
func (g *ItemGroupBy) Scan(ctx context.Context, v any) error         { return nil }

// ItemMutation mirrors a real ent mutation builder, which also exposes
// .IDs(ctx). Its receiver is *ItemMutation (not ...Query), so the analyzer
// must NOT flag it — mutations are not subject to the limit interceptor.
type ItemMutation struct{}

func (m *ItemMutation) IDs(ctx context.Context) ([]int, error) { return nil, nil }

type Item struct{}

type EventClient struct{}

func (*EventClient) Query() *EventQuery { return &EventQuery{} }

type EventQuery struct{}

func (q *EventQuery) All(ctx context.Context) ([]*Event, error) { return nil, nil }
func (q *EventQuery) IDs(ctx context.Context) ([]int, error)    { return nil, nil }
func (q *EventQuery) Limit(int) *EventQuery                     { return q }
func (q *EventQuery) Select(...string) *EventSelect             { return &EventSelect{EventQuery: q} }

type EventSelect struct{ *EventQuery }

func (s *EventSelect) Strings(ctx context.Context) ([]string, error) { return nil, nil }

type Event struct{}
