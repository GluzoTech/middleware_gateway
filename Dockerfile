# syntax=docker/dockerfile:1

# ---- Build stage -----------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src

RUN apk add --no-cache ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/server ./cmd/server

# Runtime data directories, owned by the unprivileged runtime user.
RUN mkdir -p /out/storage/logs /out/storage/workflows

# ---- Runtime stage ---------------------------------------------------------
# Distroless: no shell, no package manager, runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/server /app/server
COPY --from=build --chown=nonroot:nonroot /out/storage /app/storage

ENV APP_PORT=8080 \
    LOG_DIRECTORY=/app/storage/logs \
    WORKFLOW_DIRECTORY=/app/storage/workflows

EXPOSE 8080
VOLUME ["/app/storage"]
USER nonroot:nonroot

ENTRYPOINT ["/app/server"]
