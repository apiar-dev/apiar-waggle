# L'image de waggle : la commande, le client Docker et Compose pour lever les instances, et iptables pour le pare-feu d'un
# serveur. `bin/waggle` la construit depuis ce dépôt ; rien d'autre à installer qu'un Docker.

FROM golang:1.27-alpine AS construction
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" -o /waggle ./cmd/waggle

# Les essais : Go, le client Docker et Compose, pour la suite d'intégration qui lève de vraies instances (Makefile)
FROM golang:1.27-alpine AS essais
RUN apk add --no-cache docker-cli docker-cli-buildx docker-cli-compose
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM docker:29-cli
RUN apk add --no-cache iptables iptables-legacy
COPY --from=construction /waggle /usr/local/bin/waggle
ENTRYPOINT ["waggle"]
