.PHONY: lint
lint: golangci-lint govulncheck

.PHONY: test
test:
	@echo "Running test..."
	go test -race ./...

.PHONY: golangci-lint
golangci-lint:
	@echo "Running golangci-lint..."
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run

.PHONY: govulncheck
govulncheck:
	@echo "Running govulncheck..."
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

