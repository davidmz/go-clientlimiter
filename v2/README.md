# go-clientlimiter v2

A two-level rate limiter for Go with global and per-client limits.

## What's New in v2

v2 introduces a cleaner API with allocation-free operation and better resource management:

### Key Changes from v1

- **`Releaser` struct** instead of `Closer` interface
- **`Release() bool`** instead of `Close() error` 
- **Allocation-free operation** - no memory allocations during acquire/release cycles
- **Better idempotency handling** - prevents double-release safely
- **Safe copying** - releasers can now be copied without issues

### API Comparison

| v1 | v2 |
|---|---|
| `ok, closer := limiter.Acquire(id)` | `ok, rel := limiter.Acquire(id)` |
| `err := closer.Close()` | `released := rel.Release()` |
| `Closer` interface | `Releaser` struct |
| Returns `error` | Returns `bool` |

## Installation

```bash
go get github.com/davidmz/go-clientlimiter/v2
```

## Quick Example

```go
package main

import (
    "fmt"
    "time"
    
    "github.com/davidmz/go-clientlimiter/v2"
)

func main() {
    // Create limiter: 10 global, 3 per client, 1 second timeout
    limiter := clientlimiter.NewLimiter[string](10, 3, time.Second)
    
    // Acquire resource
    if ok, rel := limiter.Acquire("client1"); ok {
        defer rel.Release() // release is idempotent, returns bool
        // Do work...
        fmt.Println("Work completed")
    } else {
        fmt.Println("Rate limited")
    }
}
```

## Advanced Features

### Custom Limits Per Client

```go
limiter := clientlimiter.NewLimiter[string](10, 2, time.Second)

// Set custom limits
limiter.ClientLimitByKey = func(clientID string) int {
    switch clientID {
    case "premium":
        return 5
    case "basic":
        return 1
    default:
        return 2
    }
}
```

### Custom Timeouts Per Client

```go
limiter.DelayByKey = func(clientID string) time.Duration {
    switch clientID {
    case "fast":
        return 100 * time.Millisecond
    case "slow":
        return 5 * time.Second
    default:
        return time.Second
    }
}
```

### Monitoring Client Load

```go
used, total := limiter.ClientLoad("client1")
fmt.Printf("Client load: %d/%d\n", used, total)
```

### Automatic Cleanup

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// Clean up unused client semaphores every minute
limiter.StartPeriodicCleanup(ctx, time.Minute)
```

## Migration from v1

1. **Update import path:**
   ```go
   // v1
   import "github.com/davidmz/go-clientlimiter"
   
   // v2
   import "github.com/davidmz/go-clientlimiter/v2"
   ```

2. **Update variable names:**
   ```go
   // v1
   ok, closer := limiter.Acquire("client1")
   
   // v2
   ok, rel := limiter.Acquire("client1")
   ```

3. **Update release calls:**
   ```go
   // v1
   if err := closer.Close(); err != nil {
       log.Printf("Error: %v", err)
   }
   
   // v2
   if rel.Release() {
       // successfully released
   }
   ```

## License

MIT
