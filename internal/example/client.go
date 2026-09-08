package example

import (
	"context"
	"errors"
)

// Thing is the placeholder domain object — rename it to whatever your
// upstream system stores (Alert, Dashboard, Series, …).
type Thing struct {
	ID   string
	Name string
}

// Client is the contract tools depend on. Keep it small: tools test against
// this interface via FakeClient (client_fake.go); only the production impl
// talks to a real upstream.
type Client interface {
	List(ctx context.Context) ([]Thing, error)
	Get(ctx context.Context, id string) (Thing, error)
	Create(ctx context.Context, name string) (Thing, error)
}

// ErrNotFound is returned by Get when the id does not exist.
var ErrNotFound = errors.New("thing not found")
