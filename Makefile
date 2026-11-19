.PHONY: build test cover e2e e2e-rootless proto
build:
	go build -o bin/parker ./cmd/parker
test:
	go test ./...
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
e2e: build
	for f in $$(grep -v '^#' hack/e2e/suite.txt); do hack/rootless-e2e.sh "$$f" || exit 1; done
e2e-rootless: build
	hack/rootless-mode-e2e.sh examples/minimal.yaml
proto:
	buf generate
