// Package clientlimiter provides a two-level rate limiter with global and
// per-client limits. It supports timeout-based acquisition and automatic
// cleanup of unused client semaphores.
package clientlimiter

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/davidmz/go-clientlimiter/timerpool"
)

// Releaser releases acquired resources. Zero value is a no-op.
// The Release method is idempotent for a given value. Avoid copying after use,
// as copies maintain independent idempotency flags.
type Releaser struct {
	globalSem chan struct{}
	clientSem chan struct{}
	closed    atomic.Bool
}

// Release returns tokens to client and global semaphores. Safe to call multiple
// times.
func (r *Releaser) Release() {
	if r == nil || r.clientSem == nil || r.globalSem == nil {
		return
	}
	if r.closed.Swap(true) {
		return
	}
	select {
	case <-r.clientSem:
	default:
	}
	select {
	case <-r.globalSem:
	default:
	}
}

// Limiter provides two-level rate limiting: global and per-client. K is the
// type of client identifier (must be comparable for use as map key).
type Limiter[K comparable] struct {
	globalSem    chan struct{}       // global semaphore limiting total concurrent operations
	clientSems   map[K]chan struct{} // per-client semaphores
	maxPerClient int                 // maximum concurrent operations per client
	maxDelay     time.Duration       // maximum time to wait for resource acquisition
	mu           sync.RWMutex        // protects clientSems map
	timers       *timerpool.TimerPool
}

// NewLimiter creates a new two-level rate limiter. globalLimit: maximum
// concurrent operations across all clients perClientLimit: maximum concurrent
// operations per individual client maxDelay: maximum time to wait when
// acquiring resources
func NewLimiter[K comparable](globalLimit, perClientLimit int, maxDelay time.Duration) *Limiter[K] {
	return &Limiter[K]{
		globalSem:    make(chan struct{}, globalLimit),
		clientSems:   make(map[K]chan struct{}),
		maxPerClient: perClientLimit,
		maxDelay:     maxDelay,
		timers:       timerpool.New(),
	}
}

// Acquire tries to secure a resource for the given client, adhering to both
// global and per-client constraints with a specified timeout. Returns: ok and
// releaser value. If ok is false, releaser will be zero-value and calling
// Release on it will be a no-op.
func (l *Limiter[K]) Acquire(clientID K) (ok bool, rel Releaser) {
	// Double-checked locking pattern to find or create client semaphore
	l.mu.RLock()
	clientSem, ok := l.clientSems[clientID]
	l.mu.RUnlock()
	if !ok {
		// Client semaphore doesn't exist, create it under write lock
		l.mu.Lock()
		clientSem, ok = l.clientSems[clientID] // check again under write lock
		if !ok {
			clientSem = make(chan struct{}, l.maxPerClient)
			l.clientSems[clientID] = clientSem
		}
		l.mu.Unlock()
	}

	// Acquire resources under read lock (semaphores are safe for concurrent
	// use)
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Fast happy path (non-blocking): try to acquire both semaphores
	select {
	case l.globalSem <- struct{}{}:
		select {
		case clientSem <- struct{}{}:
			return true, Releaser{globalSem: l.globalSem, clientSem: clientSem}
		default:
			// Client full: release global and fail
			<-l.globalSem
			return false, Releaser{}
		}
	default:
		// Global full: proceed to slow path
	}

	// Slow path with timeout
	timer := l.timers.Get(l.maxDelay)
	defer timer.Put()
	timeout := timer.C()

	// Try global first with timeout
	select {
	case l.globalSem <- struct{}{}:
		// proceed to client
	case <-timeout:
		return false, Releaser{}
	}

	// Try client with timeout
	select {
	case clientSem <- struct{}{}:
		return true, Releaser{globalSem: l.globalSem, clientSem: clientSem}
	case <-timeout:
		// Client timeout: release global and fail
		<-l.globalSem
		return false, Releaser{}
	}
}

// ClientLoad returns the current load for a specific client in a non-blocking
// way. It returns the number of currently used tokens and the total capacity
// for the client. If the client has never acquired resources, it returns (0,
// maxPerClient).
func (l *Limiter[K]) ClientLoad(clientID K) (used, total int) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	clientSem := l.clientSems[clientID]
	return len(clientSem), l.maxPerClient
}

// StartPeriodicCleanup starts a background goroutine that periodically removes
// unused client semaphores to prevent memory leaks. The goroutine stops when
// the provided context is cancelled.
func (l *Limiter[K]) StartPeriodicCleanup(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.cleanup()
			}
		}
	}()
}

// cleanup removes unused client semaphores (private method).
func (l *Limiter[K]) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Remove client semaphores with no active tokens
	for id, sem := range l.clientSems {
		if len(sem) == 0 {
			delete(l.clientSems, id)
		}
	}
}
