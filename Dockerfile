FROM alpine:latest

# Default: follow the latest GitHub release. Override with
# `--build-arg YACTT_VERSION=vX.Y.Z` to pin a specific tag.
ARG YACTT_VERSION=latest

# curl for the release download, ca-certificates for TLS to github.com.
# No libc6-compat: release.yml's `linux/arm64+musl` matrix entry
# produces a fully static binary (`-extldflags=-static` plus
# `CC=musl-gcc`) that links against musl directly, so the runtime
# only needs musl — which Alpine ships by default. No glibc
# compatibility shim required.
RUN apk add --no-cache curl ca-certificates

# Fetch yactt release, verify SHA-256, install. The musl-linked
# `yactt_linux_arm64_musl` tarball is the arm64 artifact shipped by
# release.yml for musl-libc hosts (Alpine, distroless, scratch).
# The SHA256SUMS file lists the tarball under that full name, so we
# keep the same basename on disk for sha256sum -c to find. Rename the
# extracted binary so `yactt` resolves on PATH for CMD. grep -P (PCRE)
# is unavailable on Alpine/BusyBox, so use sed to parse the tag_name
# from the API.
RUN set -eux; \
    if [ "${YACTT_VERSION}" = "latest" ]; then \
        YACTT_RELEASE=$(curl -fsSL https://api.github.com/repos/kellenff/yactt/releases/latest \
            | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p'); \
    else \
        YACTT_RELEASE="${YACTT_VERSION}"; \
    fi; \
    [ -n "${YACTT_RELEASE:-}" ] || { echo "no tag resolved (YACTT_VERSION=${YACTT_VERSION})" >&2; exit 1; }; \
    curl -fsSL "https://github.com/kellenff/yactt/releases/download/${YACTT_RELEASE}/yactt_linux_arm64_musl.tar.gz" -o yactt_linux_arm64_musl.tar.gz; \
    curl -fsSL "https://github.com/kellenff/yactt/releases/download/${YACTT_RELEASE}/SHA256SUMS"                     -o SHA256SUMS; \
    grep "yactt_linux_arm64_musl.tar.gz" SHA256SUMS | sha256sum -c -; \
    tar -xzf yactt_linux_arm64_musl.tar.gz -C /usr/local/bin; \
    mv /usr/local/bin/yactt_linux_arm64_musl /usr/local/bin/yactt; \
    rm yactt_linux_arm64_musl.tar.gz SHA256SUMS

EXPOSE 8000

CMD ["yactt", "mcp", "serve-http"]
