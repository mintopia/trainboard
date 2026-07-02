.PHONY: test vet lint check build

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

check: vet lint test

build:
	go build ./...
