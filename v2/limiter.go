// Package clientlimiter provides a two-level rate limiter with global and
// per-client limits. It supports timeout-based acquisition and automatic
// cleanup of unused client semaphores.
package clientlimiter

import (
	"context"
	"sync"
	"time"

	"github.com/davidmz/go-clientlimiter/v2/onetime"
	"github.com/davidmz/go-clientlimiter/v2/timerpool"
)

// Releaser releases acquired resources. Zero value is a no-op.
// The Release method is idempotent and safe to call multiple times.
// Copying is now safe as release state is tracked via onetime tokens.
type Releaser struct {
	globalSem chan struct{}
	clientSem chan struct{}
	token     onetime.Token
}

// Release returns tokens to client and global semaphores. Safe to call multiple
// times. Returns true if resources were actually released, false if already released.
func (r *Releaser) Release() bool {
	if r == nil ||
		r.clientSem == nil ||
		r.globalSem == nil ||
		!r.token.Release() { // already released
		return false
	}
	select {
	case <-r.clientSem:
	default:
	}
	select {
	case <-r.globalSem:
	default:
	}
	return true
}

// Limiter provides two-level rate limiting: global and per-client. K is the
// type of client identifier (must be comparable for use as map key).
type Limiter[K comparable] struct {
	ClientLimitByKey func(clientID K) int           // optional function to get per-client limit
	DelayByKey       func(clientID K) time.Duration // optional function to get per-client delay

	globalSem    chan struct{} // global semaphore limiting total concurrent operations
	clientSems   sync.Map      // per-client semaphores
	maxPerClient int           // default maximum concurrent operations per client
	maxDelay     time.Duration // default maximum time to wait for resource acquisition
	mu           sync.RWMutex  // protects against cleanup during acquire and load operations
	timers       *timerpool.TimerPool
	tokensPool   *onetime.Registry
}

// NewLimiter creates a new two-level rate limiter. globalLimit: maximum
// concurrent operations across all clients perClientLimit: maximum concurrent
// operations per individual client maxDelay: maximum time to wait when
// acquiring resources
func NewLimiter[K comparable](globalLimit, perClientLimit int, maxDelay time.Duration) *Limiter[K] {
	return &Limiter[K]{
		globalSem:    make(chan struct{}, globalLimit),
		maxPerClient: perClientLimit,
		maxDelay:     maxDelay,
		timers:       timerpool.New(),
		tokensPool:   onetime.NewRegistry(),
	}
}

// Acquire tries to secure a resource for the given client, adhering to both
// global and per-client constraints with a specified timeout. Returns: ok and
// releaser value. If ok is false, releaser will be zero-value and calling
// Release on it will be a no-op.
func (l *Limiter[K]) Acquire(clientID K) (ok bool, rel Releaser) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Get client-specific limit and delay
	clientLimit := l.maxPerClient
	if l.ClientLimitByKey != nil {
		clientLimit = l.ClientLimitByKey(clientID)
	}

	clientDelay := l.maxDelay
	if l.DelayByKey != nil {
		clientDelay = l.DelayByKey(clientID)
	}

	clientSem, found := l.clientSems.Load(clientID)
	if !found {
		clientSem, _ = l.clientSems.LoadOrStore(clientID, make(chan struct{}, clientLimit))
	}

	// Fast path: try to acquire both semaphores without blocking
	select {
	case l.globalSem <- struct{}{}:
		select {
		case clientSem.(chan struct{}) <- struct{}{}:
			return true, Releaser{
				globalSem: l.globalSem,
				clientSem: clientSem.(chan struct{}),
				token:     l.tokensPool.Acquire(),
			}
		default:
			// Client semaphore full, release global and fall through to slow path
			<-l.globalSem
		}
	default:
		// Global semaphore full, fall through to slow path
	}

	// Slow path with timeout
	timer := l.timers.Get(clientDelay)
	defer timer.Put()
	timeout := timer.C()

	// Try global first with timeout
	select {
	case l.globalSem <- struct{}{}:
		// proceed to client
	case <-timeout:
		return
	}

	// Try client with timeout
	select {
	case clientSem.(chan struct{}) <- struct{}{}:
		return true, Releaser{
			globalSem: l.globalSem,
			clientSem: clientSem.(chan struct{}),
			token:     l.tokensPool.Acquire(),
		}
	case <-timeout:
		// Client timeout: release global and fail
		<-l.globalSem
		return
	}
}

// ClientLoad returns the current load for a specific client in a non-blocking
// way. It returns the number of currently used tokens and the total capacity
// for the client. If the client has never acquired resources, it returns (0,
// maxPerClient).
func (l *Limiter[K]) ClientLoad(clientID K) (used, total int) {
	// Get client-specific limit
	clientLimit := l.maxPerClient
	if l.ClientLimitByKey != nil {
		clientLimit = l.ClientLimitByKey(clientID)
	}

	clientSem, found := l.clientSems.Load(clientID)
	if !found {
		return 0, clientLimit
	}
	return len(clientSem.(chan struct{})), clientLimit
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
	l.clientSems.Range(func(key, value interface{}) bool {
		sem := value.(chan struct{})
		if len(sem) == 0 {
			l.clientSems.Delete(key)
		}
		return true
	})
}
