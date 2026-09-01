SHELL := /bin/bash
VERSION := 0.70.0-beta.70+osw-v013-gui-native-1080p
OSWORLD_BASE_IMAGE ?= osw-hybrid:v0.14
DOCKER_BUILD_NETWORK ?= host
PYTHON ?= python3

.PHONY: agents build build-local build-host build-all test verify smoke smoke-v1-direct image-v1

agents:
	$(PYTHON) scripts/download-agent-artifacts.py

build:
	./scripts/build-release.sh

build-local:
	mkdir -p artifacts/releases/v0.14
	cd computer-use && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o ../artifacts/releases/v0.14/computer-use .
	cp releases/v0.14/release.json artifacts/releases/v0.14/release.json

# Standalone cross-platform developer artifacts. These are deliberately kept
# outside the frozen v0.14 Linux release identity.
build-host:
	mkdir -p artifacts/development
	cd computer-use && CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)+dev" \
		-o ../artifacts/development/computer-use .

build-all:
	mkdir -p artifacts/development
	cd computer-use && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)+dev" \
		-o ../artifacts/development/computer-use-linux-amd64 .
	cd computer-use && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)+dev" \
		-o ../artifacts/development/computer-use-darwin-amd64 .
	cd computer-use && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)+dev" \
		-o ../artifacts/development/computer-use-darwin-arm64 .
	cd computer-use && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)+dev" \
		-o ../artifacts/development/computer-use-windows-amd64.exe .

test:
	cd computer-use && go test ./...
	cd computer-use && $(PYTHON) -m unittest -q runtime_tests.test_portable_protocol
	$(PYTHON) -m pytest -q tests

verify:
	$(PYTHON) scripts/verify-release.py

smoke:
	$(PYTHON) scripts/smoke-mcp.py artifacts/releases/v0.14/computer-use

image-v1:
	docker build \
		--network "$(DOCKER_BUILD_NETWORK)" \
		--build-arg OSWORLD_BASE_IMAGE="$(OSWORLD_BASE_IMAGE)" \
		-t hybrid-computer-use:v0.14-source \
		-f docker/Dockerfile.osworld-v1 .

smoke-v1-direct:
	$(PYTHON) scripts/smoke-v1-direct.py --image hybrid-computer-use:v0.14-source
