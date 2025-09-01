# go-clientlimiter (v1)

⚠️ **This is version 1 of go-clientlimiter. The current version is v2 with improved API.**

**For new projects, use v2:** [/v2/README.md](v2/README.md)

---

A two-level rate limiter for Go with global and per-client limits.

## Installation (v1)

```bash
go get github.com/davidmz/go-clientlimiter
```

## Quick Example (v1)

```go
package main

import (
    "fmt"
    "time"
    
    "github.com/davidmz/go-clientlimiter"
)

func main() {
    // Create limiter: 10 global, 3 per client, 1 second timeout
    limiter := clientlimiter.NewLimiter[string](10, 3, time.Second)
    
    // Acquire resource
    if ok, closer := limiter.Acquire("client1"); ok {
        defer closer.Close() // close is idempotent, returns error
        // Do work...
        fmt.Println("Work completed")
    } else {
        fmt.Println("Rate limited")
    }
}
```

## Migration to v2

See [v2 documentation](v2/README.md) for the new API with:
- `Releaser` struct instead of `Closer` interface
- `Release() bool` instead of `Close() error`
- Allocation-free operation
- Better idempotency handling
- Safe copying of releasers

## License

MIT
