# Build stage
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /app

# Copy dependency files first for layer caching
COPY backend/go.mod backend/go.sum ./
RUN go mod download

# Copy source code
COPY backend/ ./

# Build the binaries. omo-healthcheck is a tiny HTTP probe baked into the
# image so docker-compose's HEALTHCHECK directive has something it can
# invoke inside the distroless/static runtime (which has no shell or curl).
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /omo-core ./cmd/omo-core && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /omo-healthcheck ./cmd/omo-healthcheck

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=builder /omo-core /omo-core
COPY --from=builder /omo-healthcheck /omo-healthcheck
COPY migrations/ /migrations/
COPY configs/ /configs/

EXPOSE 8080

USER nonroot:nonroot

# Image-level healthcheck fallback. The authoritative healthcheck lives in
# docker-compose.yml so the interval/timeout can be tuned per environment.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD ["/omo-healthcheck"]

ENTRYPOINT ["/omo-core"]
