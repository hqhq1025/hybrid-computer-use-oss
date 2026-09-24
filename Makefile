SHELL := /bin/bash
VERSION := 0.70.0-beta.70+osw-v013-gui-native-1080p
OSWORLD_BASE_IMAGE ?= osw-hybrid:v0.14
DOCKER_BUILD_NETWORK ?= host

.PHONY: agents build build-local test smoke smoke-v1-direct image-v1

agents:
	python3 scripts/download-agent-artifacts.py

build:
	./scripts/build-release.sh

build-local:
	mkdir -p artifacts
	cd computer-use && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o ../artifacts/computer-use .

test:
	cd computer-use && go test ./...
	python3 -m pytest -q tests

smoke:
	python3 scripts/smoke-mcp.py artifacts/computer-use

image-v1:
	docker build \
		--network "$(DOCKER_BUILD_NETWORK)" \
		--build-arg OSWORLD_BASE_IMAGE="$(OSWORLD_BASE_IMAGE)" \
		-t hybrid-computer-use:v0.14-source \
		-f docker/Dockerfile.osworld-v1 .

smoke-v1-direct:
	python3 scripts/smoke-v1-direct.py --image hybrid-computer-use:v0.14-source
