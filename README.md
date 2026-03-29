# pprof

A simple, safe, and production-ready library for enabling **pprof** profiling in Go applications.

---

## Possibilities

- Full support for **all** standard pprof endpoints (`/debug/pprof/`)
- Convenient handler registration into any existing `http.ServeMux`
- Graceful shutdown with `context.Context` support
- Secure default settings (`127.0.0.1:6060`)
- Clean, well-tested, and well-documented code
- Minimalist and user-friendly API

## Installation

Simply add the package to your project:

```bash
github.com/jwm1rr0rb10/go-pprof
```

---

## Usage
1. Registering with an existing HTTP server (recommended method)


```go
package main

import (
	"net/http"

	"github.com/jwm1rr0rb10/go-pprof"
)

func main() {
	mux := http.NewServeMux()

	// Add all pprof endpoints in a single line
	pprof.Register(mux)

	// Your standard handlers
	mux.HandleFunc("/api/health", healthHandler)
	mux.HandleFunc("/api/users", usersHandler)

	http.ListenAndServe(":8080", mux)
}
```

Profiling is now available at:
`http://localhost:8080/debug/pprof/`

---

## 2. Standalone pprof Server (Separate Port)

```go
package main

import (
	"context"
	"log"
	"time"

	"github.com/jwm1rr0rb10/go-pprof"
)

func main() {
	// You can use NewConfig("", 0, 0) — default values ​​will be applied.
	cfg := pprof.NewConfig("127.0.0.1", 6060, 10*time.Second)
	server := pprof.NewServer(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the pprof server in a separate goroutine.
	go func() {
		if err := server.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("pprof server error: %v", err)
		}
	}()

	// ... your application's main code ...

	// Upon application termination, gracefully stop the pprof server.
	<-someShutdownSignal
	cancel()
}
```

---

## Configuration

```go
cfg := pprof.NewConfig(host, port, readHeaderTimeout)


// Default values ​​(if empty values ​​are passed):
// Host:              "127.0.0.1"
// Port:              6060
// ReadHeaderTimeout: 10 * time.Second
```

---

## Лучшие практики

- Never expose pprof to the internet—it contains sensitive information about your application.
- Run it only on localhost or within a private network/VPC.
- It is recommended to use `pprof.Register(mux)` instead of a separate server.
- The standard port for pprof is 6060.

---

## Доступные эндпоинты

| Эндпоинт                  | Описание                            |
|:--------------------------|:------------------------------------|
| /debug/pprof/             | Home Page (Index)                   |
| /debug/pprof/profile      | CPU Profile (30 seconds by default) |
| /debug/pprof/heap         | Heap snapshot                       |
| /debug/pprof/allocs       | Object Allocations                  |
| /debug/pprof/goroutine    | Goroutine stack                     |
| /debug/pprof/block        | Blocking Operations                 |
| /debug/pprof/mutex        | Mutex Contention                    |
| /debug/pprof/threadcreate | Created Streams                     |
| /debug/pprof/trace        | Execution Tracex                    |
| /debug/pprof/cmdline      | Command Line                   |
| /debug/pprof/symbol       | Symbols (for Instruments)          |

---

## Testing

```bash
go test ./...
```

---

## ## License
[MIT License](https://github.com/jwm1rr0rb10/go-pprof/blob/main/LICENSE) – © Raman Zaitsau [@jwm1rrr0rb10](https://github.com/jwm1rr0rb10)

