BINARY := bin/adapter
IMAGE ?= alertmanager-feishu-adapter:test

.PHONY: fmt fmt-check vet test test-race check build image image-amd64

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

fmt-check:
	@test -z "$$(gofmt -l $$(find cmd internal -name '*.go' -type f))" || \
		(echo "Go files need formatting; run 'make fmt'" && exit 1)

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

check: fmt-check vet test-race build

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/adapter

image:
	docker build -t $(IMAGE) .

image-amd64:
	docker build --platform linux/amd64 -t $(IMAGE) .
