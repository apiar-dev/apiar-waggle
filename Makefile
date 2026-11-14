# Everything runs in Docker: there is neither Go nor anything else to install.
GO_IMAGE := golang:1.27-alpine
GO := docker run --rm -v "$(CURDIR):/src" -v waggle-go-cache:/go -w /src $(GO_IMAGE)

.PHONY: test check image integration firewall shellcheck

## Unit tests
test:
	$(GO) go test ./...

## Formatting, go vet, and unit tests
check:
	@test -z "$$($(GO) gofmt -l .)" || { $(GO) gofmt -l .; echo "gofmt: files to format"; exit 1; }
	$(GO) go vet ./...
	$(GO) go vet -tags integration ./tests/integration/
	$(GO) go test ./...

image:
	docker build --tag waggle:local .

## The integration suite: real instances, on offset ports, with throwaway stacks (tests/integration)
## On Linux: WAGGLE_TEST_HOST=127.0.0.1, and the host network
WAGGLE_TEST_HOST ?= host.docker.internal
integration: image
	docker build --quiet --target test --tag waggle:test . >/dev/null
	docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v "$(CURDIR):$(CURDIR)" -v waggle-go-cache:/go \
		-w "$(CURDIR)" -e WAGGLE_TEST_HOST=$(WAGGLE_TEST_HOST) -e WAGGLE_TEST_KEEP -e WAGGLE_OS=$$(uname -s) \
		waggle:test go test -tags integration -count=1 -v -timeout 15m ./tests/integration/

## The firewall rule, in an isolated Docker-in-Docker (tests/firewall)
firewall: image
	sh tests/firewall/test.sh

shellcheck:
	docker run --rm -v "$(CURDIR):/mnt" -w /mnt koalaman/shellcheck:stable bin/waggle install.sh tests/firewall/test.sh
