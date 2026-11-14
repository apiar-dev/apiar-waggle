# waggle's image: the command, the Docker client and Compose to start the instances, and iptables for a server's
# firewall. `bin/waggle` builds it from this repository; nothing to install but Docker.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" -o /waggle ./cmd/waggle

# Tests: Go, the Docker client and Compose, for the integration suite that starts real instances (Makefile)
FROM golang:1.27-alpine AS test
RUN apk add --no-cache docker-cli docker-cli-buildx docker-cli-compose
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM docker:29-cli
RUN apk add --no-cache iptables iptables-legacy
COPY --from=build /waggle /usr/local/bin/waggle
ENTRYPOINT ["waggle"]
