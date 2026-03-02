# Build stage
FROM golang:1.22-bookworm AS builder

WORKDIR /app

# Copy dependency files first for layer caching
COPY backend/go.mod backend/go.sum ./
RUN go mod download

# Copy source code
COPY backend/ ./

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /omo-core ./cmd/omo-core

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /omo-core /omo-core
COPY migrations/ /migrations/

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/omo-core"]
