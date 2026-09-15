VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/bifrost-datahub-exporter ./cmd/exporter

test:
	go test ./...

lint:
	go vet ./...

docker:
	docker build -t bifrost-datahub-exporter:$(VERSION) .
