.PHONY: all fmt vet test race cover lint tags

all: fmt vet race

fmt:
	gofmt -s -w .

vet:
	go vet ./...

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

lint:
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

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
