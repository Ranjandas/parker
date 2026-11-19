.PHONY: build test cover e2e e2e-rootless proto
build:
	go build -o bin/parker ./cmd/parker
test:
	go test ./...
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
e2e: build
	hack/rootless-e2e.sh examples/minimal.yaml
	hack/rootless-e2e.sh examples/clients.yaml
	hack/rootless-e2e.sh examples/federation.yaml
e2e-rootless: build
	hack/rootless-mode-e2e.sh examples/minimal.yaml
proto:
	buf generate
