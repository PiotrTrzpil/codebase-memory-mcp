.PHONY: build test lint clean install check

BINARY=codebase-memory-mcp
MODULE=github.com/DeusData/codebase-memory-mcp

build:
	go build -o bin/$(BINARY) ./cmd/codebase-memory-mcp/

test:
	go test ./... -v

check: lint test  ## Run lint + tests

lint:  ## Run golangci-lint
	golangci-lint run --timeout=5m ./...

clean:
	rm -rf bin/

install:
	@pkill -f 'codebase-memory-mcp$$' 2>/dev/null && sleep 0.3 || true
	go install ./cmd/codebase-memory-mcp/
	@if [ -d "$(HOME)/.local/bin" ] && [ "$$(go env GOPATH)/bin" != "$(HOME)/.local/bin" ]; then \
		cp "$$(go env GOPATH)/bin/$(BINARY)" "$(HOME)/.local/bin/$(BINARY)"; \
	fi
