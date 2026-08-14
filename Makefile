GO ?= go
NPM ?= npm
AGENT_VERSION ?= dev
LDFLAGS := -X gopit/internal/agent/system.AgentVersion=$(AGENT_VERSION)

.PHONY: build-agent build-server build-all test vet clean dev-server dev-web release-linux

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

release-linux: clean web/dist
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/gopit ./cmd/gopit
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/gopitd ./cmd/gopitd
	chmod +x bin/gopit bin/gopitd
	sha256sum bin/gopit > gopit.sha256
	sha256sum bin/gopitd > gopitd.sha256
	sha256sum bin/gopit bin/gopitd > gopit-linux-amd64.sha256

dev-server:
	$(GO) run ./cmd/gopit -config configs/gopit.yaml

dev-web:
	cd web && $(NPM) run dev