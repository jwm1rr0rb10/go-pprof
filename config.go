package pprof

import "time"

// Default values applied by [NewServer] when the corresponding
// [Config] field is left at its zero value.
const (
	DefaultHost              = "127.0.0.1"
	DefaultPort              = 6060
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultIdleTimeout       = 60 * time.Second
	DefaultShutdownTimeout   = 15 * time.Second
	maxHeaderBytes           = 64 << 10
)

// Recommended settings for Config.BlockProfileRate and
// Config.MutexProfileFraction on a busy service: enough samples to find
// contention, with low constant overhead. Rate 1 / fraction 1 record every
// event and are too expensive for production.
const (
	// Blocking events of 10µs and longer are always recorded; shorter ones
	// are sampled with probability duration/10µs.
	RecommendedBlockProfileRate = 10_000
	// On average 1 of 100 mutex contention events is recorded.
	RecommendedMutexProfileFraction = 100
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

	// IdleTimeout closes idle keep-alive connections.
	// 0 means DefaultIdleTimeout (60s).
	IdleTimeout time.Duration

	// ShutdownTimeout bounds the graceful shutdown performed when the
	// context passed to [Server.Run] or [Server.Serve] is canceled.
	ShutdownTimeout time.Duration

	// Middlewares wrap every pprof handler. The first one is the outermost.
	// They run before the Limits checks, so put authentication here.
	Middlewares []Middleware

	// Limits protect the service from expensive requests. The zero value
	// is a safe production configuration; see [Limits].
	Limits Limits

	// BlockProfileRate, if > 0, is passed to runtime.SetBlockProfileRate
	// when the server starts, enabling /debug/pprof/block.
	// See RecommendedBlockProfileRate. This is a process-wide setting.
	BlockProfileRate int

	// MutexProfileFraction, if > 0, is passed to
	// runtime.SetMutexProfileFraction when the server starts, enabling
	// /debug/pprof/mutex. See RecommendedMutexProfileFraction.
	// This is a process-wide setting.
	MutexProfileFraction int

	// ResetProfileRates, if true, undoes BlockProfileRate and
	// MutexProfileFraction when [Server.Run] or [Server.Serve] returns: the
	// block profile rate is set to 0 (the runtime cannot report the previous
	// value) and the mutex profile fraction is restored to what it was.
	// Use it when profiling is switched on only for a debugging session.
	ResetProfileRates bool
}

// NewConfig creates a Config with the most common fields set.
// Zero values are replaced with defaults by [NewServer]. For other fields
// (Limits, Middlewares, ...) use a Config struct literal.
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
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = DefaultShutdownTimeout
	}
	return c
}
