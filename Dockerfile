FROM golang:1.19-alpine3.16 AS builder
WORKDIR /go/src/app

COPY go.mod go.sum ./
RUN go mod download

ENV CGO_ENABLED=0
COPY . .
RUN go build ./cmd/...

FROM alpine:3.17.0

COPY --from=builder /go/src/app/default-operator-config.yaml /opt/operator/config.yaml
COPY --from=builder /go/src/app/spicedb-operator /usr/local/bin/spicedb-operator
ENTRYPOINT ["spicedb-operator"]
