# Developer shortcuts. The release pipeline itself is .goreleaser.yaml + .github/workflows/release.yml.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test vet install dist clean
build:            ## build ./bin/sentinel for this machine
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/sentinel ./cmd/sentinel
test:             ## vet and test (with the race detector)
	go vet ./...
	go test ./... -race -count=1
install: build    ## copy to ~/.local/bin
	install -d $(HOME)/.local/bin
	install -m 0755 bin/sentinel $(HOME)/.local/bin/sentinel
dist:             ## all release archives + checksums into ./dist (no GoReleaser needed)
	scripts/build-dist.sh $(VERSION)
clean:
	rm -rf bin dist
