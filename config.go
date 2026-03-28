package pprof

import "time"

// Config holds the configuration for the pprof server.
type Config struct {
	Host              string        // e.g. "127.0.0.1" or "localhost"
	Port              int           // e.g. 6060 (conventional pprof port)
	ReadHeaderTimeout time.Duration // protects against slow clients
}

// NewConfig creates a new Config with sensible defaults applied later in NewServer.
func NewConfig(host string, port int, readHeaderTimeout time.Duration) Config {
	return Config{
		Host:              host,
		Port:              port,
		ReadHeaderTimeout: readHeaderTimeout,
	}
}
