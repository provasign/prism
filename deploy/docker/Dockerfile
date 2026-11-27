# syntax=docker/dockerfile:1
#
# Prism image: a static `prism` binary on distroless, non-root. Its main use
# is the link server (`prism link-server`, see docs/LINK_SERVER.md). The same
# binary also runs `prism index` and `prism workspace push` in CI.
#
# Build from the repository root (the build context must be the root):
#
#   docker build -f deploy/docker/Dockerfile --build-arg VERSION=v1.4.0 -t prism:v1.4.0 .
#   docker buildx build -f deploy/docker/Dockerfile --platform linux/amd64,linux/arm64 \
#     --build-arg VERSION=v1.4.0 -t ghcr.io/provasign/prism:v1.4.0 --push .
#
# Build arguments:
#   VERSION     version stamped into the binary (internal/version.Version). Default "dev".
#   GO_VERSION  Go toolchain image tag for the build stage. Keep it equal to
#               GO_VERSION in .github/workflows/release.yml. Default 1.26.11.
#   TARGETARCH, BUILDARCH, BUILDPLATFORM
#               set by BuildKit from --platform and the build host. Do not pass them.
#
# Why cgo: Prism links tree-sitter parsers written in C, so CGO_ENABLED=0 does
# not build. The binary is linked fully static instead, as the release job
# does it (-tags netgo, -extldflags "-static"). It needs no libc and runs on
# distroless/static. The build stage runs on the build host's platform and
# cross-compiles with a Debian cross gcc, so a multi-arch build needs no QEMU.
#
# Run it:
#   docker run --rm prism:v1.4.0                       # prints `prism link-server help`
#   docker run -p 8890:8890 -p 9890:9890 -v store:/data -v ./tokens:/etc/prism:ro prism:v1.4.0 \
#     link-server --dir /data --tokens /etc/prism/tokens.json --ops-addr :9890

ARG GO_VERSION=1.26.11

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build
ARG TARGETARCH
ARG BUILDARCH
RUN set -eux; \
    if [ "${TARGETARCH}" != "${BUILDARCH}" ]; then \
      case "${TARGETARCH}" in \
        arm64) pkgs="gcc-aarch64-linux-gnu libc6-dev-arm64-cross" ;; \
        amd64) pkgs="gcc-x86-64-linux-gnu libc6-dev-amd64-cross" ;; \
        *) echo "unsupported TARGETARCH ${TARGETARCH}"; exit 1 ;; \
      esac; \
      apt-get update; apt-get install -y --no-install-recommends ${pkgs}; rm -rf /var/lib/apt/lists/*; \
    fi
WORKDIR /src
ENV GOWORK=off GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    case "${TARGETARCH}" in \
      "${BUILDARCH}") cc=gcc ;; \
      arm64) cc=aarch64-linux-gnu-gcc ;; \
      amd64) cc=x86_64-linux-gnu-gcc ;; \
    esac; \
    CGO_ENABLED=1 GOOS=linux GOARCH="${TARGETARCH}" CC="${cc}" \
    go build -trimpath -buildvcs=false -tags netgo \
      -ldflags="-s -w -X github.com/provasign/prism/internal/version.Version=${VERSION} -extldflags \"-static\"" \
      -o /out/prism ./cmd/prism; \
    mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="prism" \
      org.opencontainers.image.description="Prism code-context CLI and link server" \
      org.opencontainers.image.source="https://github.com/provasign/prism" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/prism /usr/local/bin/prism
# A writable, nonroot-owned default for `--dir /data`. Mount a volume here.
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
# 8890: the link server API. 9890: --ops-addr (/health, /metrics) by convention.
EXPOSE 8890 9890
ENTRYPOINT ["prism"]
CMD ["link-server", "help"]
