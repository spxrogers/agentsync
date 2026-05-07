.PHONY: build test lint fmt tidy ci clean

build:
	go build -o bin/agentsync ./cmd/agentsync

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w -s .
	go run mvdan.cc/gofumpt@latest -w .

tidy:
	go mod tidy

ci: lint test
	goreleaser release --snapshot --skip publish --clean

clean:
	rm -rf bin/ dist/ coverage.out coverage.html
