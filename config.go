package pprof

import "time"

// Default values applied by [NewServer] when the corresponding
// [Config] field is left at its zero value.
const (
	DefaultHost              = "127.0.0.1"
	DefaultPort              = 6060
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultShutdownTimeout   = 15 * time.Second
)

// Config holds the configuration for the standalone pprof [Server].
//
// Zero values are replaced with the Default* constants, so Config{} is a
// valid configuration that listens on 127.0.0.1:6060.
type Config struct {
	// Host to listen on, e.g. "127.0.0.1", "localhost" or "::1".
	// Avoid "" / "0.0.0.0" on untrusted networks: pprof leaks sensitive data.
	Host string

	// Port to listen on. 0 means DefaultPort (6060).
	// To listen on a random free port, pass your own listener to [Server.Serve].
	Port int

	// ReadHeaderTimeout protects against slow clients (Slowloris).
	// No WriteTimeout is set on purpose: /profile and /trace stream for
	// as long as the client requests (30s by default for /profile).
	ReadHeaderTimeout time.Duration

	// ShutdownTimeout bounds the graceful shutdown performed when the
	// context passed to [Server.Run] or [Server.Serve] is canceled.
	ShutdownTimeout time.Duration

	// Middlewares wrap every pprof handler. The first one is the outermost.
	Middlewares []Middleware
}

// NewConfig creates a Config with the most common fields set.
// Zero values are replaced with defaults by [NewServer]. For other fields
// (ShutdownTimeout, Middlewares) use a Config struct literal.
func NewConfig(host string, port int, readHeaderTimeout time.Duration) Config {
	return Config{
		Host:              host,
		Port:              port,
		ReadHeaderTimeout: readHeaderTimeout,
	}
}

func (c Config) withDefaults() Config {
	if c.Host == "" {
		c.Host = DefaultHost
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.ReadHeaderTimeout <= 0 {
		c.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = DefaultShutdownTimeout
	}
	return c
}
