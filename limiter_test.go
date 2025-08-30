package clientlimiter

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNewLimiter(t *testing.T) {
	limiter := NewLimiter[string](10, 3, time.Second)

	if limiter == nil {
		t.Fatal("NewLimiter returned nil")
	}

	if limiter.maxPerClient != 3 {
		t.Errorf("Expected maxPerClient=3, got %d", limiter.maxPerClient)
	}

	if limiter.maxDelay != time.Second {
		t.Errorf("Expected maxDelay=1s, got %v", limiter.maxDelay)
	}

	if cap(limiter.globalSem) != 10 {
		t.Errorf("Expected global semaphore capacity=10, got %d", cap(limiter.globalSem))
	}
}

func TestBasicAcquireRelease(t *testing.T) {
	limiter := NewLimiter[string](5, 2, time.Second)

	// Acquire resource
	ok, rel := limiter.Acquire("client1")
	if !ok {
		t.Fatal("Failed to acquire resource")
	}

	// Release resource
	rel.Release()

	// Double release should be safe
	rel.Release()
}

func TestGlobalLimit(t *testing.T) {
	limiter := NewLimiter[string](2, 5, 100*time.Millisecond)

	// Acquire up to global limit
	ok1, rel1 := limiter.Acquire("client1")
	ok2, rel2 := limiter.Acquire("client2")

	if !ok1 || !ok2 {
		t.Fatal("Should be able to acquire up to global limit")
	}

	// Third acquisition should timeout
	start := time.Now()
	ok3, _ := limiter.Acquire("client3")
	elapsed := time.Since(start)

	if ok3 {
		t.Error("Should not be able to exceed global limit")
	}

	if elapsed < 90*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Errorf("Expected timeout around 100ms, got %v", elapsed)
	}

	// Release and try again
	rel1.Release()
	ok4, rel4 := limiter.Acquire("client4")
	if !ok4 {
		t.Error("Should be able to acquire after release")
	}

	rel2.Release()
	rel4.Release()
}

func TestPerClientLimit(t *testing.T) {
	limiter := NewLimiter[string](10, 2, 100*time.Millisecond)

	// Acquire up to per-client limit
	ok1, rel1 := limiter.Acquire("client1")
	ok2, rel2 := limiter.Acquire("client1")

	if !ok1 || !ok2 {
		t.Fatal("Should be able to acquire up to per-client limit")
	}

	// Third acquisition for same client should timeout
	start := time.Now()
	ok3, _ := limiter.Acquire("client1")
	elapsed := time.Since(start)

	if ok3 {
		t.Error("Should not be able to exceed per-client limit")
	}

	if elapsed < 90*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Errorf("Expected timeout around 100ms, got %v", elapsed)
	}

	// Different client should still work
	ok4, rel4 := limiter.Acquire("client2")
	if !ok4 {
		t.Error("Different client should be able to acquire")
	}

	rel1.Release()
	rel2.Release()
	rel4.Release()
}

func TestConcurrentAccess(t *testing.T) {
	limiter := NewLimiter[int](3, 2, 50*time.Millisecond) // Tighter limits and shorter timeout

	const numGoroutines = 10
	const numAcquisitions = 5

	var wg sync.WaitGroup
	successCount := make(chan int, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			successes := 0
			for range numAcquisitions {
				ok, rel := limiter.Acquire(clientID)
				if ok {
					successes++
					// Hold for longer to create contention
					time.Sleep(20 * time.Millisecond)
					rel.Release()
				}
			}
			successCount <- successes
		}(i % 5) // Use only 5 different client IDs to test per-client limits
	}

	wg.Wait()
	close(successCount)

	totalSuccesses := 0
	for count := range successCount {
		totalSuccesses += count
	}

	// Should have some successes but not all due to limits
	if totalSuccesses == 0 {
		t.Error("Expected some successful acquisitions")
	}

	if totalSuccesses == numGoroutines*numAcquisitions {
		t.Error("Expected some acquisitions to fail due to limits")
	}

	t.Logf("Total successful acquisitions: %d/%d", totalSuccesses, numGoroutines*numAcquisitions)
}

func TestCleanup(t *testing.T) {
	limiter := NewLimiter[string](10, 2, time.Second)

	// Acquire and release resources for multiple clients
	clients := []string{"client1", "client2", "client3"}

	for _, client := range clients {
		ok, rel := limiter.Acquire(client)
		if !ok {
			t.Fatalf("Failed to acquire for %s", client)
		}
		rel.Release()
	}

	// Check that client semaphores were created
	limiter.mu.RLock()
	initialCount := len(limiter.clientSems)
	limiter.mu.RUnlock()

	if initialCount != len(clients) {
		t.Errorf("Expected %d client semaphores, got %d", len(clients), initialCount)
	}

	// Run cleanup
	limiter.cleanup()

	// Check that empty semaphores were removed
	limiter.mu.RLock()
	finalCount := len(limiter.clientSems)
	limiter.mu.RUnlock()

	if finalCount != 0 {
		t.Errorf("Expected 0 client semaphores after cleanup, got %d", finalCount)
	}
}

func TestCleanupWithActiveSemaphores(t *testing.T) {
	limiter := NewLimiter[string](10, 2, time.Second)

	// Acquire resource and don't release
	ok1, rel1 := limiter.Acquire("client1")
	if !ok1 {
		t.Fatal("Failed to acquire for client1")
	}

	// Acquire and release for another client
	ok2, rel2 := limiter.Acquire("client2")
	if !ok2 {
		t.Fatal("Failed to acquire for client2")
	}
	rel2.Release()

	// Run cleanup
	limiter.cleanup()

	// Only empty semaphore should be removed
	limiter.mu.RLock()
	count := len(limiter.clientSems)
	_, hasClient1 := limiter.clientSems["client1"]
	_, hasClient2 := limiter.clientSems["client2"]
	limiter.mu.RUnlock()

	if count != 1 {
		t.Errorf("Expected 1 client semaphore after cleanup, got %d", count)
	}

	if !hasClient1 {
		t.Error("Active client1 semaphore should not be removed")
	}

	if hasClient2 {
		t.Error("Empty client2 semaphore should be removed")
	}

	// Clean up
	rel1.Release()
}

func TestZeroTimeout(t *testing.T) {
	limiter := NewLimiter[string](1, 1, 1*time.Nanosecond) // Very short timeout

	// First acquisition should succeed
	ok1, rel1 := limiter.Acquire("client1")
	if !ok1 {
		t.Fatal("First acquisition should succeed")
	}

	// Second acquisition should fail immediately
	start := time.Now()
	ok2, _ := limiter.Acquire("client2")
	elapsed := time.Since(start)

	if ok2 {
		t.Error("Second acquisition should fail with zero timeout")
	}

	// Should fail very quickly (within a few milliseconds)
	if elapsed > 10*time.Millisecond {
		t.Errorf("Zero timeout should fail quickly, took %v", elapsed)
	}

	rel1.Release()
}

func TestStartPeriodicCleanup(t *testing.T) {
	limiter := NewLimiter[string](10, 2, time.Second)

	// Create some client semaphores
	clients := []string{"client1", "client2", "client3"}
	for _, client := range clients {
		ok, rel := limiter.Acquire(client)
		if !ok {
			t.Fatalf("Failed to acquire for %s", client)
		}
		rel.Release()
	}

	// Verify semaphores exist
	limiter.mu.RLock()
	initialCount := len(limiter.clientSems)
	limiter.mu.RUnlock()

	if initialCount != len(clients) {
		t.Errorf("Expected %d client semaphores, got %d", len(clients), initialCount)
	}

	// Start periodic cleanup with short interval
	ctx, cancel := context.WithCancel(context.Background())
	limiter.StartPeriodicCleanup(ctx, 50*time.Millisecond)

	// Wait for cleanup to run
	time.Sleep(100 * time.Millisecond)

	// Check that semaphores were cleaned up
	limiter.mu.RLock()
	finalCount := len(limiter.clientSems)
	limiter.mu.RUnlock()

	if finalCount != 0 {
		t.Errorf("Expected 0 client semaphores after periodic cleanup, got %d", finalCount)
	}

	// Cancel context and verify goroutine stops
	cancel()
	time.Sleep(10 * time.Millisecond) // Give goroutine time to stop
}

func TestClientLoad(t *testing.T) {
	limiter := NewLimiter[string](10, 3, time.Second)

	// Test load for non-existent client
	used, total := limiter.ClientLoad("nonexistent")
	if used != 0 || total != 3 {
		t.Errorf("Expected (0, 3) for non-existent client, got (%d, %d)", used, total)
	}

	// Acquire one resource
	ok1, rel1 := limiter.Acquire("client1")
	if !ok1 {
		t.Fatal("Failed to acquire first resource")
	}

	used, total = limiter.ClientLoad("client1")
	if used != 1 || total != 3 {
		t.Errorf("Expected (1, 3) after first acquire, got (%d, %d)", used, total)
	}

	// Acquire second resource
	ok2, rel2 := limiter.Acquire("client1")
	if !ok2 {
		t.Fatal("Failed to acquire second resource")
	}

	used, total = limiter.ClientLoad("client1")
	if used != 2 || total != 3 {
		t.Errorf("Expected (2, 3) after second acquire, got (%d, %d)", used, total)
	}

	// Acquire third resource (should reach limit)
	ok3, rel3 := limiter.Acquire("client1")
	if !ok3 {
		t.Fatal("Failed to acquire third resource")
	}

	used, total = limiter.ClientLoad("client1")
	if used != 3 || total != 3 {
		t.Errorf("Expected (3, 3) after third acquire, got (%d, %d)", used, total)
	}

	// Release one resource
	rel1.Release()

	used, total = limiter.ClientLoad("client1")
	if used != 2 || total != 3 {
		t.Errorf("Expected (2, 3) after first release, got (%d, %d)", used, total)
	}

	// Release all resources
	rel2.Release()
	rel3.Release()

	used, total = limiter.ClientLoad("client1")
	if used != 0 || total != 3 {
		t.Errorf("Expected (0, 3) after all releases, got (%d, %d)", used, total)
	}

	// Test different client
	ok4, rel4 := limiter.Acquire("client2")
	if !ok4 {
		t.Fatal("Failed to acquire for client2")
	}

	// Check both clients
	used1, total1 := limiter.ClientLoad("client1")
	used2, total2 := limiter.ClientLoad("client2")

	if used1 != 0 || total1 != 3 {
		t.Errorf("Expected (0, 3) for client1, got (%d, %d)", used1, total1)
	}

	if used2 != 1 || total2 != 3 {
		t.Errorf("Expected (1, 3) for client2, got (%d, %d)", used2, total2)
	}

	rel4.Release()
}

func TestClientLoadConcurrent(t *testing.T) {
	limiter := NewLimiter[int](10, 2, time.Second)

	const numGoroutines = 5
	var wg sync.WaitGroup

	// Start goroutines that acquire and release resources
	for i := range numGoroutines {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			for range 10 {
				ok, rel := limiter.Acquire(clientID)
				if ok {
					// Check load while holding resource
					used, total := limiter.ClientLoad(clientID)
					if used < 0 || used > total {
						t.Errorf("Invalid load: used=%d, total=%d", used, total)
					}

					time.Sleep(1 * time.Millisecond)
					rel.Release()
				}
			}
		}(i % 3) // Use 3 different client IDs
	}

	// Concurrently check loads
	wg.Add(1)
	go func() {
		defer wg.Done()

		for range 50 {
			for clientID := range 3 {
				used, total := limiter.ClientLoad(clientID)
				if used < 0 || used > total || total != 2 {
					t.Errorf("Invalid load for client %d: used=%d, total=%d", clientID, used, total)
				}
			}
			time.Sleep(1 * time.Millisecond)
		}
	}()

	wg.Wait()
}

func BenchmarkAcquireRelease(b *testing.B) {
	limiter := NewLimiter[int](100, 10, time.Second)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		clientID := 0
		for pb.Next() {
			if ok, rel := limiter.Acquire(clientID); ok {
				rel.Release()
			}
			clientID = (clientID + 1) % 10 // Rotate through 10 clients
		}
	})
}

func BenchmarkHighContention(b *testing.B) {
	limiter := NewLimiter[int](2, 1, 100*time.Millisecond) // Very limited

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		clientID := 0
		for pb.Next() {
			if ok, rel := limiter.Acquire(clientID); ok {
				rel.Release()
			}
			clientID = (clientID + 1) % 100 // Many clients competing
		}
	})
}
