FROM golang:1.18 AS builder
WORKDIR /go/src/app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build ./cmd/...

FROM alpine:3.15

COPY --from=builder /go/src/app/authzed-operator /usr/local/bin/authzed-operator
ENTRYPOINT ["authzed-enterprise-operator"]