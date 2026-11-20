.PHONY: build test cover e2e e2e-rootless e2e-ent proto
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
# Needs a Consul Enterprise license (CONSUL_LICENSE_PATH or CONSUL_LICENSE);
# skips without one. Not part of CI yet.
e2e-ent: build
	hack/ent-e2e.sh
	hack/ent-e2e.sh examples/partitions.yaml
	hack/ent-e2e.sh examples/peering-partitions.yaml
proto:
	buf generate
