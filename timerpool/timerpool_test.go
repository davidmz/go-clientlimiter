package timerpool

import (
	"sync"
	"testing"
	"time"
)

// TestBasicFire ensures that a timer obtained from the pool fires within a reasonable time window.
func TestBasicFire(t *testing.T) {
	tp := New()
	pt := tp.Get(50 * time.Millisecond)
	defer pt.Put()

	start := time.Now()
	select {
	case <-pt.C():
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timer didn't fire within expected time")
	}
	_ = time.Since(start) // Keep elapsed for potential future assertions if needed
}

// TestReuseNoStaleTick verifies that after consuming a tick and returning the timer to the pool,
// the next checkout does not observe a stale tick immediately.
func TestReuseNoStaleTick(t *testing.T) {
	tp := New()

	// First use: short timer that we fully consume
	pt1 := tp.Get(5 * time.Millisecond)
	select {
	case <-pt1.C():
		// consumed
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("first timer didn't fire")
	}
	pt1.Put()

	// Second use: should not be immediately ready
	pt2 := tp.Get(50 * time.Millisecond)
	defer pt2.Put()

	select {
	case <-pt2.C():
		t.Fatalf("unexpected immediate tick: stale value left in channel")
	default:
		// not ready yet, as expected
	}

	// Eventually it should fire
	select {
	case <-pt2.C():
		// ok
	case <-time.After(1 * time.Second):
		t.Fatalf("second timer didn't fire")
	}
}

// TestDrainOnPutWithoutConsume ensures that Put drains the timer's channel when the
// timer has already fired but the user didn't consume from the channel.
func TestDrainOnPutWithoutConsume(t *testing.T) {
	tp := New()

	pt := tp.Get(10 * time.Millisecond)
	// Wait long enough so the timer fires, but DO NOT read from pt.C().
	time.Sleep(50 * time.Millisecond)
	pt.Put()

	// Next Get should not observe the old tick.
	pt2 := tp.Get(40 * time.Millisecond)
	defer pt2.Put()

	select {
	case <-pt2.C():
		t.Fatalf("unexpected immediate tick after Put without consume: channel was not drained")
	default:
		// ok
	}

	select {
	case <-pt2.C():
		// ok
	case <-time.After(1 * time.Second):
		t.Fatalf("timer didn't fire after reuse")
	}
}

// TestConcurrentGetPut stresses the pool with concurrent Get/Put cycles
// to catch potential data races or deadlocks.
func TestConcurrentGetPut(t *testing.T) {
	tp := New()

	const goroutines = 16
	const rounds = 50
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				pt := tp.Get(2 * time.Millisecond)
				select {
				case <-pt.C():
					// ok
				case <-time.After(250 * time.Millisecond):
					t.Errorf("worker timeout waiting for timer")
				}
				pt.Put()
			}
		}()
	}

	wg.Wait()
}
