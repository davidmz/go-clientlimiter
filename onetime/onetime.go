package onetime

import (
	"sync"
	"sync/atomic"
)

// Registry manages a pool of reusable slots for one-time tokens.
// It provides efficient allocation and deallocation of tokens by reusing
// underlying slot objects.
type Registry struct {
	pool sync.Pool
}

// NewRegistry creates a new token registry with an initialized object pool.
// Each slot created by the pool maintains a reference back to this registry.
func NewRegistry() *Registry {
	r := &Registry{}
	r.pool = sync.Pool{New: func() any { return &slot{owner: r} }}
	return r
}

// Token represents a one-time use token that can be released exactly once.
// Each token has a unique generation number that prevents double-release.
type Token struct {
	gen  uint32 // generation number for this specific token instance
	slot *slot  // reference to the underlying slot from the pool
}

// slot represents a reusable container for tokens from the object pool.
// The state field packs both generation counter and closed flag into a single atomic value.
type slot struct {
	state atomic.Uint64 // packed: generation (lower 32 bits) + closed flag (MSB)
	owner *Registry     // reference back to the registry for pool operations
}

// Acquire obtains a new one-time token from the registry.
// It gets a slot from the pool and atomically increments its generation counter.
// The loop ensures thread-safe generation increment using compare-and-swap.
func (r *Registry) Acquire() Token {
	slot := r.pool.Get().(*slot)
	for {
		state := slot.state.Load()
		newState := mkState(stGen(state)+1, false) // increment generation, mark as open
		// Atomically update state if it hasn't changed since we read it
		if slot.state.CompareAndSwap(state, newState) {
			return Token{gen: stGen(newState), slot: slot}
		}
		// If CAS failed, another goroutine modified the state - retry
	}
}

// Release marks the token as used and returns its slot to the pool.
// This method is idempotent - calling it multiple times is safe.
// It uses generation numbers to prevent double-release of the same token.
// Returns true if the token was successfully released, false if it was already released.
func (t *Token) Release() bool {
	if t == nil || t.slot == nil {
		return false
	}
	for {
		prev := t.slot.state.Load()
		// Check if token was already released or generation doesn't match
		if stGen(prev) != t.gen || stClosed(prev) {
			return false // already released or invalid token
		}
		newState := mkState(t.gen, true) // mark as closed, keep same generation
		// Atomically mark as closed and return to pool
		if t.slot.state.CompareAndSwap(prev, newState) {
			t.slot.owner.pool.Put(t.slot) // return slot to pool for reuse
			return true
		}
		// If CAS failed, state changed - retry the check
	}
}

// closedBit is the most significant bit of a uint64
const closedBit = 1 << 63

// mkState creates a state value from a generation and a closed flag
func mkState(gen uint32, closed bool) uint64 {
	v := uint64(gen)
	if closed {
		v |= closedBit
	}
	return v
}

// stGen returns the generation number from a state value
func stGen(v uint64) uint32 { return uint32(v) }

// stClosed returns the closed flag from a state value
func stClosed(v uint64) bool { return v&closedBit != 0 }
