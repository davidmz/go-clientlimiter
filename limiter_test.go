package clientlimiter

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mapSize returns the number of elements in a sync.Map
func mapSize(m *sync.Map) int {
	count := 0
	m.Range(func(key, value any) bool {
		count++
		return true
	})
	return count
}

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
	initialCount := mapSize(&limiter.clientSems)
	limiter.mu.RUnlock()

	if initialCount != len(clients) {
		t.Errorf("Expected %d client semaphores, got %d", len(clients), initialCount)
	}

	// Run cleanup
	limiter.cleanup()

	// Check that empty semaphores were removed
	limiter.mu.RLock()
	finalCount := mapSize(&limiter.clientSems)
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
	count := mapSize(&limiter.clientSems)
	_, hasClient1 := limiter.clientSems.Load("client1")
	_, hasClient2 := limiter.clientSems.Load("client2")
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
	initialCount := mapSize(&limiter.clientSems)
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
	finalCount := mapSize(&limiter.clientSems)
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

func TestClientLimitByKey(t *testing.T) {
	limiter := NewLimiter[string](10, 2, time.Second)

	// Set custom limits for different clients
	limiter.ClientLimitByKey = func(clientID string) int {
		switch clientID {
		case "premium":
			return 5
		case "basic":
			return 1
		default:
			return 2 // default
		}
	}

	// Test premium client can acquire up to 5 resources
	var premiumRels []*Releaser
	for i := range 5 {
		ok, rel := limiter.Acquire("premium")
		if !ok {
			t.Fatalf("Premium client should be able to acquire %d resources, failed at %d", 5, i+1)
		}
		premiumRels = append(premiumRels, &rel)
	}

	// 6th acquisition should fail for premium
	ok, _ := limiter.Acquire("premium")
	if ok {
		t.Error("Premium client should not be able to acquire more than 5 resources")
	}

	// Test basic client can acquire only 1 resource
	ok1, rel1 := limiter.Acquire("basic")
	if !ok1 {
		t.Fatal("Basic client should be able to acquire 1 resource")
	}

	ok2, _ := limiter.Acquire("basic")
	if ok2 {
		t.Error("Basic client should not be able to acquire more than 1 resource")
	}

	// Test default client gets default limit (2)
	ok3, rel3 := limiter.Acquire("default")
	ok4, rel4 := limiter.Acquire("default")
	if !ok3 || !ok4 {
		t.Fatal("Default client should be able to acquire 2 resources")
	}

	ok5, _ := limiter.Acquire("default")
	if ok5 {
		t.Error("Default client should not be able to acquire more than 2 resources")
	}

	// Test ClientLoad returns correct limits
	used, total := limiter.ClientLoad("premium")
	if used != 5 || total != 5 {
		t.Errorf("Expected premium client load (5, 5), got (%d, %d)", used, total)
	}

	used, total = limiter.ClientLoad("basic")
	if used != 1 || total != 1 {
		t.Errorf("Expected basic client load (1, 1), got (%d, %d)", used, total)
	}

	used, total = limiter.ClientLoad("default")
	if used != 2 || total != 2 {
		t.Errorf("Expected default client load (2, 2), got (%d, %d)", used, total)
	}

	// Test non-existent client gets custom limit
	used, total = limiter.ClientLoad("nonexistent")
	if used != 0 || total != 2 {
		t.Errorf("Expected nonexistent client load (0, 2), got (%d, %d)", used, total)
	}

	// Clean up
	for _, rel := range premiumRels {
		rel.Release()
	}
	rel1.Release()
	rel3.Release()
	rel4.Release()
}

func TestDelayByKey(t *testing.T) {
	limiter := NewLimiter[string](1, 1, 100*time.Millisecond) // Global limit of 1

	// Set custom delays for different clients
	limiter.DelayByKey = func(clientID string) time.Duration {
		switch clientID {
		case "fast":
			return 10 * time.Millisecond
		case "slow":
			return 200 * time.Millisecond
		default:
			return 100 * time.Millisecond // default
		}
	}

	// Acquire the only global resource
	ok1, rel1 := limiter.Acquire("holder")
	if !ok1 {
		t.Fatal("Should be able to acquire first resource")
	}

	// Test fast client times out quickly
	start := time.Now()
	ok2, _ := limiter.Acquire("fast")
	elapsed := time.Since(start)

	if ok2 {
		t.Error("Fast client should timeout")
	}

	if elapsed < 5*time.Millisecond || elapsed > 50*time.Millisecond {
		t.Errorf("Fast client should timeout around 10ms, took %v", elapsed)
	}

	// Test slow client times out slowly
	start = time.Now()
	ok3, _ := limiter.Acquire("slow")
	elapsed = time.Since(start)

	if ok3 {
		t.Error("Slow client should timeout")
	}

	if elapsed < 150*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Errorf("Slow client should timeout around 200ms, took %v", elapsed)
	}

	// Test default client uses default delay
	start = time.Now()
	ok4, _ := limiter.Acquire("default")
	elapsed = time.Since(start)

	if ok4 {
		t.Error("Default client should timeout")
	}

	if elapsed < 80*time.Millisecond || elapsed > 150*time.Millisecond {
		t.Errorf("Default client should timeout around 100ms, took %v", elapsed)
	}

	rel1.Release()
}

func TestClientLimitByKeyWithCleanup(t *testing.T) {
	limiter := NewLimiter[string](10, 2, time.Second)

	// Set custom limits
	limiter.ClientLimitByKey = func(clientID string) int {
		if clientID == "large" {
			return 5
		}
		return 1
	}

	// Create semaphores with different sizes
	ok1, rel1 := limiter.Acquire("large")
	ok2, rel2 := limiter.Acquire("small")

	if !ok1 || !ok2 {
		t.Fatal("Should be able to acquire resources")
	}

	// Release resources
	rel1.Release()
	rel2.Release()

	// Check that semaphores were created
	limiter.mu.RLock()
	initialCount := mapSize(&limiter.clientSems)
	limiter.mu.RUnlock()

	if initialCount != 2 {
		t.Errorf("Expected 2 client semaphores, got %d", initialCount)
	}

	// Run cleanup - both should be removed as they're empty
	limiter.cleanup()

	limiter.mu.RLock()
	finalCount := mapSize(&limiter.clientSems)
	limiter.mu.RUnlock()

	if finalCount != 0 {
		t.Errorf("Expected 0 client semaphores after cleanup, got %d", finalCount)
	}
}
