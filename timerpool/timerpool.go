// Package timerpool provides a pool for reusing time.Timer instances. Reusing
// timers reduces allocations and GC pressure in code that starts short-lived
// timers frequently.
//
// Usage:
//
//	tp := timerpool.New()
//	pt := tp.Get(250*time.Millisecond)
//	defer pt.Put() // always return timer to the pool
//	select {
//	case <-pt.C():
//	    // timer fired
//	case <-ctx.Done():
//	    // cancelled or timeout elsewhere
//	}
package timerpool

import (
	"sync"
	"time"
)

// TimerPool is a reusable pool of time.Timer objects. Timers taken from the
// pool are properly stopped and drained before use, making Reset safe and
// avoiding leaks of timer events.
type TimerPool struct {
	pool sync.Pool
}

// New returns a new TimerPool. Each timer is created once and immediately
// stopped and drained so that it can be safely Reset when checked out from the
// pool.
func New() *TimerPool {
	return &TimerPool{
		pool: sync.Pool{
			New: func() any {
				t := time.NewTimer(time.Hour)
				// Stop the fresh timer and drain its channel if needed. A
				// timer's channel must be drained if Stop reports false,
				// otherwise future Resets may observe a stale tick.
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
				return t
			},
		},
	}
}

// PooledTimer wraps a *time.Timer acquired from a TimerPool and remembers the
// pool it should be returned to.
type PooledTimer struct {
	timer *time.Timer
	pool  *TimerPool
}

// Get returns a pooled timer already Reset for the given interval. Before
// resetting we ensure the timer is stopped and its channel is drained to
// satisfy the Reset rules in the time package.
func (tp *TimerPool) Get(interval time.Duration) *PooledTimer {
	t := tp.pool.Get().(*time.Timer)
	// Ensure the timer is in a clean state before Reset:
	// Stop returns whether the timer was active; if it wasn't, we must drain.
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(interval)
	return &PooledTimer{timer: t, pool: tp}
}

// C returns the underlying timer's channel.
func (pt *PooledTimer) C() <-chan time.Time {
	return pt.timer.C
}

// Put stops the timer (draining its channel if needed) and returns it to the pool.
func (pt *PooledTimer) Put() {
	// As with Get, we follow the Stop-and-drain pattern to avoid leaving a tick
	// in the channel, which would affect the next user of this timer.
	if !pt.timer.Stop() {
		select {
		case <-pt.timer.C:
		default:
		}
	}
	pt.pool.pool.Put(pt.timer)
}
