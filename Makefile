.PHONY: build dist test run-serve clean

GO ?= $(HOME)/sdk/go/bin/go
ifeq ($(wildcard $(GO)),)
GO := go
endif

VERSION ?= 0.1.0
LDFLAGS := -s -w -X houdry/internal/version.Version=$(VERSION)
export CGO_ENABLED := 0

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/houdry ./cmd/houdry

test:
	$(GO) test ./...

dist: build
	mkdir -p dist
	GOOS=linux   GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-linux-amd64       ./cmd/houdry
	GOOS=linux   GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-linux-arm64       ./cmd/houdry
	GOOS=darwin  GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-darwin-amd64      ./cmd/houdry
	GOOS=darwin  GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-darwin-arm64      ./cmd/houdry
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-windows-amd64.exe ./cmd/houdry
	GOOS=windows GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-windows-arm64.exe ./cmd/houdry
	cp bin/houdry dist/houdry-linux-$$($(GO) env GOARCH) 2>/dev/null || true

run-serve: build
	./bin/houdry serve --listen 0.0.0.0:8080 --binaries dist

clean:
	rm -rf bin dist
