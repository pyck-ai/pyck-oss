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

type Event struct{}
