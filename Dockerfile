# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /build

COPY go.mod ./
COPY *.go ./
COPY static ./static

# CGO_ENABLED=0 + -a produces a fully static binary; -s -w strip debug info.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -a \
    -ldflags "-extldflags '-static' -s -w" \
    -o reddit-curator .

# ── Stage 2: CA certificates ──────────────────────────────────────────────────
FROM alpine:3.19 AS certs
RUN apk add --no-cache ca-certificates

# ── Stage 3: Scratch (zero-OS) final image ────────────────────────────────────
FROM scratch

# CA bundle for HTTPS to Reddit, RedGifs, Imgur, etc.
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /build/reddit-curator /reddit-curator

EXPOSE 8080

# Health check runs the binary itself — no shell or wget needed in scratch.
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD ["/reddit-curator", "-health"]

CMD ["/reddit-curator"]
