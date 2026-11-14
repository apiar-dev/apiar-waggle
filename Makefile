# Tout tourne dans Docker : il n'y a ni Go ni rien d'autre à installer.
GO_IMAGE := golang:1.27-alpine
GO := docker run --rm -v "$(CURDIR):/src" -v waggle-go-cache:/go -w /src $(GO_IMAGE)

.PHONY: test verifier image integration pare-feu shellcheck

## Les tests unitaires
test:
	$(GO) go test ./...

## Le format, go vet, et les tests unitaires
verifier:
	@test -z "$$($(GO) gofmt -l .)" || { $(GO) gofmt -l .; echo "gofmt : fichiers à formater"; exit 1; }
	$(GO) go vet ./...
	$(GO) go test ./...

image:
	docker build --tag waggle:local .

## La suite d'intégration : de vraies instances, sur des ports décalés, et des stacks jetables (tests/integration)
## Sur Linux, WAGGLE_ESSAI_HOTE=127.0.0.1 et le réseau de l'hôte
WAGGLE_ESSAI_HOTE ?= host.docker.internal
integration: image
	docker build --quiet --target essais --tag waggle:essais . >/dev/null
	docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v "$(CURDIR):$(CURDIR)" -v waggle-go-cache:/go \
		-w "$(CURDIR)" -e WAGGLE_ESSAI_HOTE=$(WAGGLE_ESSAI_HOTE) -e WAGGLE_ESSAI_GARDER -e WAGGLE_SYSTEME=$$(uname -s) \
		waggle:essais go test -tags integration -count=1 -v -timeout 15m ./tests/integration/

## La règle de pare-feu, dans un Docker-in-Docker isolé (tests/pare-feu)
pare-feu: image
	sh tests/pare-feu/essai.sh

shellcheck:
	docker run --rm -v "$(CURDIR):/mnt" -w /mnt koalaman/shellcheck:stable bin/waggle tests/pare-feu/essai.sh
