FROM golang:1.16 AS builder
WORKDIR /app
COPY . /app
RUN go install github.com/go-delve/delve/cmd/dlv@latest
WORKDIR /app