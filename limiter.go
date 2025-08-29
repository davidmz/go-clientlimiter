// Package clientlimiter provides a two-level rate limiter with global and
// per-client limits. It supports timeout-based acquisition and automatic
// cleanup of unused client semaphores.
package clientlimiter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Closer represents an interface for releasing limiter resources.
type Closer interface {
	Close() error
}

type zeroCloser struct{}

func (z *zeroCloser) Close() error { return nil }

// token represents an acquired resource that can be released. It holds direct
// references to both global and client semaphores for efficient release.
type token struct {
	globalSem chan struct{} // reference to global semaphore
	clientSem chan struct{} // reference to client-specific semaphore
	closed    int32         // atomic flag to prevent double-close
}

// Close releases the acquired resources back to both client and global
// semaphores. Returns an error if semaphores are in an inconsistent state.
// It is safe to call Close multiple times.
func (t *token) Close() error {
	// Atomic check-and-set to prevent double-close
	if !atomic.CompareAndSwapInt32(&t.closed, 0, 1) {
		return nil // already closed
	}

	// Release client semaphore token
	select {
	case <-t.clientSem:
	default:
		return errors.New("client semaphore is empty")
	}

	// Release global semaphore token
	select {
	case <-t.globalSem:
	default:
		return errors.New("global semaphore is empty")
	}
	return nil
}

// Limiter provides two-level rate limiting: global and per-client. K is the
// type of client identifier (must be comparable for use as map key).
type Limiter[K comparable] struct {
	globalSem    chan struct{}       // global semaphore limiting total concurrent operations
	clientSems   map[K]chan struct{} // per-client semaphores
	maxPerClient int                 // maximum concurrent operations per client
	maxDelay     time.Duration       // maximum time to wait for resource acquisition
	mu           sync.RWMutex        // protects clientSems map
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
	}
}

// Acquire tries to secure a resource for the given client, adhering to both
// global and per-client constraints with a specified timeout. It returns a
// boolean indicating success and a Closer which should be invoked to release
// the resource. The Closer is always non-nil, but it is a no-op if the
// acquisition failed.
func (l *Limiter[K]) Acquire(clientID K) (bool, Closer) {
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

	timer := time.NewTimer(l.maxDelay)
	defer timer.Stop()

	// Try to acquire global semaphore first
	select {
	case l.globalSem <- struct{}{}:
		// Global acquired, now try client semaphore
		select {
		case clientSem <- struct{}{}:
			// Both acquired successfully
			return true, &token{
				globalSem: l.globalSem,
				clientSem: clientSem,
			}
		case <-timer.C:
			// Client semaphore timeout, release global
			<-l.globalSem
			return false, &zeroCloser{}
		}
	case <-timer.C:
		// Global semaphore timeout
		return false, &zeroCloser{}
	}
}

// ClientLoad returns the current load for a specific client in a non-blocking way.
// It returns the number of currently used tokens and the total capacity for the client.
// If the client has never acquired resources, it returns (0, maxPerClient).
// This method is useful for monitoring and load balancing decisions.
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
