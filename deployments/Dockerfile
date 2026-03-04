# Build stage
FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /app

# Copy dependency files first for layer caching
COPY backend/go.mod backend/go.sum ./
RUN go mod download

# Copy source code
COPY backend/ ./

# Build the binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /omo-core ./cmd/omo-core

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=builder /omo-core /omo-core
COPY migrations/ /migrations/
COPY configs/ /configs/

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/omo-core"]
