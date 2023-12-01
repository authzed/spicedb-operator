FROM golang:1.21-alpine3.18 AS builder
WORKDIR /go/src/app
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build --mount=type=cache,target=/go/pkg/mod go build ./cmd/...

FROM alpine:3.18.5

COPY --from=builder /go/src/app/validated-update-graph.yaml /opt/operator/config.yaml
COPY --from=builder /go/src/app/spicedb-operator /usr/local/bin/spicedb-operator
ENTRYPOINT ["spicedb-operator"]
