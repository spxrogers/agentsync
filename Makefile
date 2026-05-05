.PHONY: build test test-e2e test-bdd test-all test-container lint fmt tidy ci clean

build:
	go build -o bin/agentsync ./cmd/agentsync

# Default `test` keeps the fast unit + integration loop snappy. The slower
# behaviour-locking suites (e2e, bdd) are opt-in below.
test:
	go test -race ./...

# Build-tagged lifecycle e2e — exercises the binary end-to-end.
test-e2e:
	go test -tags=e2e -count=1 ./test/e2e/...

# Build-tagged Gherkin BDD suite — the authoritative behaviour lock.
test-bdd:
	go test -tags=bdd -count=1 ./test/bdd/...

# Local convenience: run every gate on the host. Mirrors what the container
# entrypoint runs, minus the hermetic isolation. Use `make test-container`
# for the release-safe gate.
test-all: test test-e2e test-bdd

# The release-readiness gate. Runs the entire test suite inside a hermetic
# container (podman preferred, docker fallback). If this passes, ship.
test-container:
	./scripts/test-in-container.sh

lint:
	golangci-lint run ./...

fmt:
	gofmt -w -s .
	go run mvdan.cc/gofumpt@latest -w .

tidy:
	go mod tidy

ci: lint test test-e2e test-bdd
	goreleaser release --snapshot --skip publish --clean

clean:
	rm -rf bin/ dist/ coverage.out coverage.html
