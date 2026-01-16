# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install dependencies
RUN apk add --no-cache git make

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binaries
RUN CGO_ENABLED=0 GOOS=linux go build -a -tags netgo -ldflags '-w -s' -o /tmp/ksa-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -a -tags netgo -ldflags '-w -s' -o /tmp/ksa ./cmd/cli

# Runtime stage
FROM alpine:3.19

# Install CA certificates and SSH client
RUN apk add --no-cache ca-certificates openssh-client

# Create non-root user
RUN addgroup -g 1000 ksa && \
    adduser -u 1000 -G ksa -s /bin/sh -D ksa

# Create directories
RUN mkdir -p /etc/ksa /var/lib/ksa /home/ksa/.ssh && \
    chown -R ksa:ksa /etc/ksa /var/lib/ksa /home/ksa/.ssh

# Copy binaries from builder
COPY --from=builder /tmp/ksa-server /usr/local/bin/ksa-server
COPY --from=builder /tmp/ksa /usr/local/bin/ksa

# Copy default config
COPY --from=builder /app/config.yaml.example /etc/ksa/config.yaml.example

# Switch to non-root user
USER ksa

# Expose gRPC port
EXPOSE 443 9090

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:9090/metrics || exit 1

# Entrypoint
ENTRYPOINT ["ksa-server"]
CMD ["--config", "/etc/ksa/config.yaml"]
