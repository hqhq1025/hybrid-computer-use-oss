SHELL := /bin/bash
VERSION := $(shell cat VERSION)
DESKTOP_IMAGE ?= hybrid-computer-use-desktop:$(VERSION)
DOCKER_BUILD_NETWORK ?= host
SMOKE_NETWORK ?= bridge

.PHONY: agents build build-local test smoke desktop-image smoke-desktop

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
	cd computer-use && test -z "$$(gofmt -l .)" && go vet ./... && go test ./...
	cd computer-use && python3 runtime_test.py
	python3 -m pytest -q tests

smoke:
	python3 scripts/smoke-mcp.py artifacts/computer-use

desktop-image: build
	@test -n "$(OSWORLD_BASE_IMAGE)" || { \
		echo "Set OSWORLD_BASE_IMAGE to a desktop image that runs the OSWorld guest server." >&2; \
		exit 1; }
	docker build \
		--network "$(DOCKER_BUILD_NETWORK)" \
		--build-arg OSWORLD_BASE_IMAGE="$(OSWORLD_BASE_IMAGE)" \
		-t "$(DESKTOP_IMAGE)" \
		-f docker/Dockerfile.desktop .

smoke-desktop:
	python3 scripts/smoke-desktop.py --image "$(DESKTOP_IMAGE)" --network "$(SMOKE_NETWORK)"
