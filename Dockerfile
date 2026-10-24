# syntax=docker/dockerfile:1

# Hardened base images, free (Docker Hardened Images, Community): building needs a free Docker account and
# `docker login dhi.io`. Without one, the same Dockerfile builds on public images:
#   docker build --build-arg BUILD_IMAGE=golang:1.27-alpine \
#                --build-arg RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot .
#
# Pinned by digest, so that a build is reproducible and a changed base image is a visible change in this file.
# Update the digests deliberately (docker buildx imagetools inspect <image:tag>); the Trivy scan in CI fails when a
# pinned base has fixable HIGH or CRITICAL findings.
ARG BUILD_IMAGE=dhi.io/golang:1.27-alpine3.23-dev@sha256:568da883960cf1ef92931413a4b5f8c424eb05b1b41384343ec54f01f404d721
ARG RUNTIME_IMAGE=dhi.io/static:20260909-alpine3.23@sha256:1e3d8b147745322567a0e76069d30d2334f98689ca5b797b87df82dae0311396

# The build stage runs on the builder's own platform and cross-compiles, so images for other architectures need
# no emulation
FROM --platform=$BUILDPLATFORM ${BUILD_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Only what the binary is built from: nothing else of the working copy reaches the build
COPY cmd ./cmd
COPY internal ./internal
COPY spec ./spec
ARG VERSION=dev
ARG TARGETOS TARGETARCH
# No cgo (SQLite is pure Go), so the binary is static and the runtime image needs no libc
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/vinyl-mind ./cmd/vinyl-mind \
 && mkdir -p /out/data

FROM ${RUNTIME_IMAGE}
LABEL org.opencontainers.image.title="vinyl-mind" \
      org.opencontainers.image.description="Hub for vinyl-check stations that learn together" \
      org.opencontainers.image.source="https://github.com/michaelpietzsch/vinyl-mind" \
      org.opencontainers.image.licenses="PolyForm-Noncommercial-1.0.0"
COPY --from=build /out/vinyl-mind /usr/local/bin/vinyl-mind
# The data directory holds the database and the server's signing key: mount a volume here and back it up
COPY --from=build --chown=65532:65532 /out/data /data
ENV VINYL_MIND_DATA_DIR=/data \
    VINYL_MIND_LISTEN=:8080
USER 65532:65532
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/vinyl-mind", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/vinyl-mind"]
CMD ["serve"]
