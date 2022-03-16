FROM golang:1.17-alpine AS builder
WORKDIR /go/src/app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build ./cmd/*

FROM alpine:3.15

RUN [ ! -e /etc/nsswitch.conf ] && echo 'hosts: files dns' > /etc/nsswitch.conf
COPY --from=builder /go/src/app/authzed-enterprise-operator /usr/local/bin/authzed-enterprise-operator
ENTRYPOINT ["authzed-enterprise-operator"]