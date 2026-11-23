# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS builder

WORKDIR /src

# Download dependencies first for better layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a static binary.
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/teal-heatmap ./cmd/server

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 appuser

COPY --from=builder /out/teal-heatmap /usr/local/bin/teal-heatmap

ENV LISTEN_ADDR=:8080

EXPOSE 8080

USER appuser

ENTRYPOINT ["/usr/local/bin/teal-heatmap"]
