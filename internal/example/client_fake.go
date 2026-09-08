package example

import (
	"context"
	"fmt"
	"sync"
)

// FakeClient is an in-memory Client for unit + integration tests. The
// production binary uses it as the default backend until you replace it
// with a real upstream client.
type FakeClient struct {
	mu     sync.Mutex
	things map[string]Thing
	next   int
}

// NewFakeClient returns an empty fake.
func NewFakeClient() *FakeClient {
	return &FakeClient{things: map[string]Thing{}}
}

// List returns every Thing in insertion-stable order.
func (c *FakeClient) List(_ context.Context) ([]Thing, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Thing, 0, len(c.things))
	for _, t := range c.things {
		out = append(out, t)
	}
	return out, nil
}

// Get returns the Thing with id, or ErrNotFound.
func (c *FakeClient) Get(_ context.Context, id string) (Thing, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.things[id]
	if !ok {
		return Thing{}, ErrNotFound
	}
	return t, nil
}

// Create adds a Thing with a synthetic ID and returns it.
func (c *FakeClient) Create(_ context.Context, name string) (Thing, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	t := Thing{ID: fmt.Sprintf("thing-%d", c.next), Name: name}
	c.things[t.ID] = t
	return t, nil
}
