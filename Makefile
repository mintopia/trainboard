.PHONY: test vet lint shellcheck check build

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

# deploy/*.sh (flash-sd.sh, migrate-to-slots.sh) currently pass shellcheck
# too, so they're included alongside deploy/image/*.sh — widen the glob
# further only after checking new scripts actually pass.
shellcheck:
	shellcheck deploy/image/*.sh deploy/*.sh

check: vet lint shellcheck test

build:
	go build ./...
