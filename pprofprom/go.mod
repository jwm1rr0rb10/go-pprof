module github.com/jwm1rr0rb10/go-pprof/pprofprom

go 1.21

require github.com/jwm1rr0rb10/go-pprof v1.2.0

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.20.5
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.63.0 // indirect
	github.com/prometheus/procfs v0.16.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	google.golang.org/protobuf v1.36.5 // indirect
)

// Development against the local copy. Consumers ignore replace directives
// and resolve the tagged v1.2.0 of the root module.
replace github.com/jwm1rr0rb10/go-pprof => ../
