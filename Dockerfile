FROM golang:1.18 AS builder
WORKDIR /go/src/app

COPY go.mod go.sum ./
RUN go mod download

ENV CGO_ENABLED=0
COPY . .
RUN go build ./cmd/...

FROM alpine:3.15

COPY --from=builder /go/src/app/spicedb-operator /usr/local/bin/spicedb-operator
ENTRYPOINT ["spicedb-operator"]
