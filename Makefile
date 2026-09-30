.PHONY: all fmt vet test race cover lint bench api vuln tags

all: fmt vet race

fmt:
	gofmt -s -w .

vet:
	go vet ./...
	cd pprofprom && go vet ./...

test:
	go test -count=1 ./...
	cd pprofprom && go test -count=1 ./...

race:
	go test -race -count=1 ./...
	cd pprofprom && go test -race -count=1 ./...

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

bench:
	go test -run='^$$' -bench=. -benchmem ./...
	cd pprofprom && go test -run='^$$' -bench=. -benchmem ./...

lint:
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...
	cd pprofprom && go run honnef.co/go/tools/cmd/staticcheck@latest ./...

# Fails if the API changed incompatibly since the latest release tag.
# gorelease needs a clean working tree: commit first.
api:
	go run golang.org/x/exp/cmd/gorelease@latest -base=$$(git describe --tags --abbrev=0 --match 'v*')

# Known vulnerabilities in reachable code, including the Go standard library.
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd pprofprom && go run golang.org/x/vuln/cmd/govulncheck@latest ./...

tags:
	@bash -c ' \
		version=$$(cat "$(CURDIR)/version" 2>/dev/null || echo "0.0.0") && \
		tag=v$$version && \
		echo "→ tag: $$tag" && \
		if [[ -n $$(git tag -l "$$tag") ]]; then \
			echo "⚠️  Tag $$tag already exists"; exit 0; \
		fi && \
		git tag -a "$$tag" -m "Release $$version" && \
		git push origin "$$tag" && \
		echo "✅ Tagged and pushed $$tag" \
	'
