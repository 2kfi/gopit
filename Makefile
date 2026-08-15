GO ?= go
NPM ?= npm
VERSION ?= dev
OS ?= $(shell $(GO) env GOOS)
ARCH ?= $(shell $(GO) env GOARCH)
AGENT_VERSION ?= $(VERSION)
LDFLAGS := -X gopit/internal/agent/system.AgentVersion=$(AGENT_VERSION) -X gopit/internal/server/api.Version=$(VERSION)

.PHONY: build-agent build-server build-all test vet clean dev-server dev-web release

build-agent:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/gopitd ./cmd/gopitd

build-server: web/dist
	$(GO) build -o bin/gopit ./cmd/gopit

build-all: web/dist
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/gopitd ./cmd/gopitd
	$(GO) build -o bin/gopit ./cmd/gopit

web/dist: web/node_modules
	cd web && $(NPM) run build

web/node_modules: web/package.json web/package-lock.json
	cd web && $(NPM) ci

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf bin web/dist web/node_modules
	rm -f gopit.db gopit.db-shm gopit.db-wal

# Build release binaries for the current GOOS/GOARCH. The workflow loops
# this over a platform matrix; unique os-arch names keep all outputs in bin/.
release: web/dist
	mkdir -p bin
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/gopit-server-$(VERSION)-$(OS)-$(ARCH) ./cmd/gopit
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/gopit-daemon-$(VERSION)-$(OS)-$(ARCH) ./cmd/gopitd
	chmod +x bin/gopit-server-$(VERSION)-$(OS)-$(ARCH) bin/gopit-daemon-$(VERSION)-$(OS)-$(ARCH)
	sha256sum bin/gopit-server-$(VERSION)-$(OS)-$(ARCH) > gopit-server-$(VERSION)-$(OS)-$(ARCH).sha256
	sha256sum bin/gopit-daemon-$(VERSION)-$(OS)-$(ARCH) > gopit-daemon-$(VERSION)-$(OS)-$(ARCH).sha256

dev-server:
	$(GO) run ./cmd/gopit -config configs/gopit.yaml

dev-web:
	cd web && $(NPM) run dev