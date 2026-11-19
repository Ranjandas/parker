.PHONY: build test e2e e2e-rootless proto
build:
	go build -o bin/parker ./cmd/parker
test:
	go test ./...
e2e: build
	hack/rootless-e2e.sh examples/minimal.yaml
	hack/rootless-e2e.sh examples/clients.yaml
e2e-rootless: build
	hack/rootless-mode-e2e.sh examples/minimal.yaml
proto:
	buf generate
