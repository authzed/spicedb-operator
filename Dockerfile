FROM golang:1.23-alpine AS builder
WORKDIR /go/src/app
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build --mount=type=cache,target=/go/pkg/mod go build ./cmd/...

FROM cgr.dev/chainguard/static:latest

COPY --from=builder /go/src/app/spicedb-operator /usr/local/bin/spicedb-operator
ENTRYPOINT ["spicedb-operator"]
