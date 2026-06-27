# ---------- Stage 1: Build ----------
FROM golang:1.22-alpine AS builder

# Required for go modules that need cgo-adjacent build tooling and for
# fetching dependencies over HTTPS.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Leverage Docker layer caching: dependencies only re-download when
# go.mod/go.sum actually change.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# Build a statically linked binary suitable for the minimal runtime image.
# CGO_ENABLED=0 avoids dynamic glibc/musl linkage issues across stages.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /out/gateway ./cmd/gateway

# ---------- Stage 2: Runtime ----------
FROM alpine:latest

# ca-certificates is required for outbound TLS calls (e.g. upstream HTTPS
# targets); tzdata supports correct local-time log timestamps if needed.
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S gateway && adduser -S gateway -G gateway

WORKDIR /app

COPY --from=builder /out/gateway /app/gateway
COPY --from=builder /src/.env.example /app/.env.example

RUN chown -R gateway:gateway /app

USER gateway

EXPOSE 8080

ENTRYPOINT ["/app/gateway"]