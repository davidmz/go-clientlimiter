# go-clientlimiter

A two-level rate limiter for Go with global and per-client limits.

## Documentation

See [pkg.go.dev](https://pkg.go.dev/github.com/davidmz/go-clientlimiter) for full documentation.

## Installation

```bash
go get github.com/davidmz/go-clientlimiter
```

## Quick Example

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
    if closer, ok := limiter.Acquire("client1"); ok {
        defer closer.Close()
        // Do work...
        fmt.Println("Work completed")
    } else {
        fmt.Println("Rate limited")
    }
}
```

## License

MIT
