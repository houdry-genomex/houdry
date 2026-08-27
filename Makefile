.PHONY: build dist dist-check test run-serve clean

GO ?= $(HOME)/sdk/go/bin/go
ifeq ($(wildcard $(GO)),)
GO := go
endif

VERSION ?= 0.6.0
LDFLAGS := -s -w -X houdry/internal/version.Version=$(VERSION)
export CGO_ENABLED := 0

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/houdry ./cmd/houdry

test:
	$(GO) test ./...

# Cross-platform artifacts consumed by scripts/install.{sh,ps1} and
# .github/workflows/release.yml. Asset names must stay:
#   houdry-<os>-<arch>[.exe]
dist: build
	mkdir -p dist
	GOOS=linux   GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-linux-amd64       ./cmd/houdry
	GOOS=linux   GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-linux-arm64       ./cmd/houdry
	GOOS=darwin  GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-darwin-amd64      ./cmd/houdry
	GOOS=darwin  GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-darwin-arm64      ./cmd/houdry
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-windows-amd64.exe ./cmd/houdry
	GOOS=windows GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/houdry-windows-arm64.exe ./cmd/houdry
	cp bin/houdry dist/houdry-linux-$$($(GO) env GOARCH) 2>/dev/null || true

# Quick local check that a dist binary is the expected release CLI.
dist-check: dist
	./dist/houdry-linux-$$($(GO) env GOARCH) version | grep -F "houdry $(VERSION)"
	./dist/houdry-linux-$$($(GO) env GOARCH) help | grep -F "houdry node join"
	./dist/houdry-linux-$$($(GO) env GOARCH) help | grep -F "houdry node list"
	./dist/houdry-linux-$$($(GO) env GOARCH) help | grep -F "houdry job submit"

run-serve: build
	./bin/houdry serve --listen 0.0.0.0:8080 --binaries dist

clean:
	rm -rf bin dist
