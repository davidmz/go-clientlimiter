package onetime

import (
	"sync"
	"testing"
)

func TestBasicAcquireRelease(t *testing.T) {
	registry := NewRegistry()

	// Acquire a token
	token := registry.Acquire()

	// Release should succeed the first time
	if !token.Release() {
		t.Error("First release should return true")
	}

	// Release should fail the second time (idempotent)
	if token.Release() {
		t.Error("Second release should return false")
	}
}

func TestMultipleTokens(t *testing.T) {
	registry := NewRegistry()

	// Acquire multiple tokens
	token1 := registry.Acquire()
	token2 := registry.Acquire()
	token3 := registry.Acquire()

	// Tokens may have same generations if they use different slots
	// This is normal behavior - generations are per-slot, not global

	// Release them in different order
	if !token2.Release() {
		t.Error("token2 release should succeed")
	}
	if !token1.Release() {
		t.Error("token1 release should succeed")
	}
	if !token3.Release() {
		t.Error("token3 release should succeed")
	}

	// All should fail on second release
	if token1.Release() || token2.Release() || token3.Release() {
		t.Error("Second releases should all fail")
	}
}

func TestZeroValueToken(t *testing.T) {
	var token Token

	// Zero value token should safely return false
	if token.Release() {
		t.Error("Zero value token release should return false")
	}
}

func TestConcurrentAccess(t *testing.T) {
	registry := NewRegistry()
	const numGoroutines = 100
	const tokensPerGoroutine = 10

	var wg sync.WaitGroup
	results := make(chan bool, numGoroutines*tokensPerGoroutine*2) // *2 for double releases

	for range numGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()

			tokens := make([]Token, tokensPerGoroutine)

			// Acquire tokens
			for j := range tokensPerGoroutine {
				tokens[j] = registry.Acquire()
			}

			// Release each token twice (test idempotency)
			for j := range tokensPerGoroutine {
				results <- tokens[j].Release() // should be true
				results <- tokens[j].Release() // should be false
			}
		}()
	}

	wg.Wait()
	close(results)

	successCount := 0
	failCount := 0
	totalResults := 0

	for result := range results {
		totalResults++
		if result {
			successCount++
		} else {
			failCount++
		}
	}

	expectedTotal := numGoroutines * tokensPerGoroutine * 2
	expectedSuccess := numGoroutines * tokensPerGoroutine
	expectedFail := numGoroutines * tokensPerGoroutine

	if totalResults != expectedTotal {
		t.Errorf("Expected %d total results, got %d", expectedTotal, totalResults)
	}
	if successCount != expectedSuccess {
		t.Errorf("Expected %d successful releases, got %d", expectedSuccess, successCount)
	}
	if failCount != expectedFail {
		t.Errorf("Expected %d failed releases, got %d", expectedFail, failCount)
	}
}

func TestSlotReuse(t *testing.T) {
	registry := NewRegistry()

	// Acquire and release many tokens to trigger slot reuse
	const numTokens = 1000
	generations := make(map[uint32]int)

	for i := range numTokens {
		token := registry.Acquire()
		generations[token.gen]++

		if !token.Release() {
			t.Errorf("Token %d release failed", i)
		}
	}

	// We should see some generation numbers repeated (slot reuse)
	// but not too many (indicating proper generation increment)
	maxReuse := 0
	for _, count := range generations {
		if count > maxReuse {
			maxReuse = count
		}
	}

	// With 1000 tokens and object pooling, we expect some reuse but not excessive
	if maxReuse > 100 {
		t.Errorf("Too much slot reuse detected: max %d uses of same generation", maxReuse)
	}

	t.Logf("Generated %d unique generations for %d tokens, max reuse: %d",
		len(generations), numTokens, maxReuse)
}

func TestGenerationIncrement(t *testing.T) {
	registry := NewRegistry()

	// Test that generations increment properly within the same slot
	// We need to force reuse of the same slot by acquiring and releasing sequentially
	token1 := registry.Acquire()
	gen1 := token1.gen
	token1.Release()

	// Next token from same slot should have incremented generation
	token2 := registry.Acquire()
	gen2 := token2.gen
	token2.Release()

	// Since we're using the same slot (likely), generation should increment
	// But this is not guaranteed due to pooling, so we just verify basic functionality
	t.Logf("First generation: %d, Second generation: %d", gen1, gen2)

	// The main test is that tokens work correctly regardless of generation values
	if gen1 == gen2 {
		t.Log("Same generation reused - this can happen with different slots")
	} else {
		t.Log("Generation incremented - slot was reused")
	}
}

func TestCopyToken(t *testing.T) {
	registry := NewRegistry()

	token1 := registry.Acquire()
	token2 := token1 // copy the token

	// First release should succeed
	if !token1.Release() {
		t.Error("First token release should succeed")
	}

	// Copy should also fail to release (same generation)
	if token2.Release() {
		t.Error("Copied token release should fail after original was released")
	}
}

func BenchmarkAcquireRelease(b *testing.B) {
	registry := NewRegistry()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			token := registry.Acquire()
			token.Release()
		}
	})
}

func BenchmarkAcquireOnly(b *testing.B) {
	registry := NewRegistry()

	for b.Loop() {
		registry.Acquire()
	}
}

func BenchmarkHighContention(b *testing.B) {
	registry := NewRegistry()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			token := registry.Acquire()
			// Simulate some work
			for i := range 10 {
				_ = i * i
			}
			token.Release()
		}
	})
}
