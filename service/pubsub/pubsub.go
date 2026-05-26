// Package pubsub provides per-key fan-out channel primitives the
// runtime kernel uses to publish events to UI subscribers. Topic
// is keyed by thread id (or any string); Broadcast is unkeyed.
//
// Both types use non-blocking sends — slow consumers drop messages
// rather than backpressure the publisher. Buffer is fixed at 16,
// large enough to absorb a UI tab's reconnect without dropping
// under normal flow, small enough that a wedged consumer doesn't
// accumulate unbounded backlog.
//
// One Topic per event shape. The runtime kernel previously
// instantiated five mechanically-identical sub/pub maps directly
// on the GraphQL resolver; this package collapses that to a single
// generic primitive with one instantiation per shape.
package pubsub

import "sync"

// Topic is a thread-safe per-key fan-out channel registry.
// Subscribe(key) creates a new buffered channel attached to that
// key; Publish(key, event) broadcasts to every subscriber for the
// key with non-blocking sends.
type Topic[T any] struct {
	mu   sync.RWMutex
	subs map[string][]chan T
}

// NewTopic constructs an empty topic.
func NewTopic[T any]() *Topic[T] {
	return &Topic[T]{
		subs: map[string][]chan T{},
	}
}

// Subscribe returns a buffered channel attached to key. The caller
// is responsible for calling Unsubscribe when done — no automatic
// cleanup on context cancellation.
func (t *Topic[T]) Subscribe(key string) chan T {
	t.mu.Lock()
	defer t.mu.Unlock()
	ch := make(chan T, 16)
	t.subs[key] = append(t.subs[key], ch)
	return ch
}

// Unsubscribe removes ch from the subscriber list for key. No-op
// if ch isn't currently subscribed under that key. Closes ch.
func (t *Topic[T]) Unsubscribe(key string, ch chan T) {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.subs[key]
	for i, s := range list {
		if s == ch {
			t.subs[key] = append(list[:i], list[i+1:]...)
			close(ch)
			return
		}
	}
}

// Publish broadcasts event to every subscriber for key with
// non-blocking sends. Subscribers whose channels are full drop the
// event. Returns the count of successful sends (may be zero).
func (t *Topic[T]) Publish(key string, event T) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var sent int
	for _, ch := range t.subs[key] {
		select {
		case ch <- event:
			sent++
		default:
		}
	}
	return sent
}

// Broadcast is the unkeyed counterpart — every subscriber gets
// every published event. Used for global event streams (thread-
// state changes that cross every UI tab).
type Broadcast[T any] struct {
	mu   sync.RWMutex
	subs []chan T
}

// NewBroadcast constructs an empty broadcast topic.
func NewBroadcast[T any]() *Broadcast[T] {
	return &Broadcast[T]{}
}

// Subscribe returns a buffered channel that will receive every
// published event. Caller must Unsubscribe when done.
func (b *Broadcast[T]) Subscribe() chan T {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan T, 16)
	b.subs = append(b.subs, ch)
	return ch
}

// Unsubscribe removes ch and closes it. No-op if ch isn't
// currently subscribed.
func (b *Broadcast[T]) Unsubscribe(ch chan T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, s := range b.subs {
		if s == ch {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			close(ch)
			return
		}
	}
}

// Publish sends event to every subscriber with non-blocking sends.
func (b *Broadcast[T]) Publish(event T) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var sent int
	for _, ch := range b.subs {
		select {
		case ch <- event:
			sent++
		default:
		}
	}
	return sent
}
