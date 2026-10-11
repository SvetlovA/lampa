# Get the latest commit branch, hash, and date
TAG := $(shell git describe --tags --abbrev=0 --exact-match 2>/dev/null)
BRANCH := $(if $(TAG),$(TAG),$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null))
HASH := $(shell git rev-parse --short=7 HEAD 2>/dev/null)
# Let git format UTC directly; GNU date -d is unavailable on macOS.
TIMESTAMP := $(shell TZ=UTC git log -1 --format=%cd --date=format-local:%Y%m%dT%H%M%S HEAD 2>/dev/null)
GIT_REV := $(BRANCH)-$(HASH)-$(TIMESTAMP)
REV := $(if $(filter --,$(GIT_REV)),latest,$(GIT_REV))

all: test build

build:
	cd cmd/lampa-api && go build -ldflags "-X main.revision=$(REV) -s -w" -o ../../.bin/lampa-api

test:
	go clean -testcache
	go test -race -coverprofile=coverage.out ./...
	grep -v "_mock.go" coverage.out | grep -v mocks > coverage_no_mocks.out
	go tool cover -func=coverage_no_mocks.out
	rm coverage.out coverage_no_mocks.out

lint:
	golangci-lint run --max-issues-per-linter=0 --max-same-issues=0

fmt:
	gofmt -s -w $$(find . -type f -name "*.go" -not -path "./vendor/*" -not -path "./mocks/*" -not -path "**/mocks/*")
	go tool goimports -w $$(find . -type f -name "*.go" -not -path "./vendor/*" -not -path "./mocks/*" -not -path "**/mocks/*")

race:
	go test -race -timeout=300s ./...

generate:
	go generate ./...

version:
	@echo "branch: $(BRANCH), hash: $(HASH), timestamp: $(TIMESTAMP)"
	@echo "revision: $(REV)"

.PHONY: all build test lint fmt race generate version
