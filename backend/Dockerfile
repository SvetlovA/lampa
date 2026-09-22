# Stage 1: Build lampa-api binary from vendored sources
FROM ghcr.io/umputun/baseimage/buildgo:v1.21.1 AS build

ARG GIT_BRANCH
ARG GITHUB_SHA
ARG CI

WORKDIR /build
ADD . /build

# the build context is backend/ without .git, so the revision comes from CI args only
RUN \
    if [ -z "$CI" ] ; then \
        echo "runs outside of CI"; \
        version="docker-$(date +%Y%m%dT%H%M%S)"; \
    else version=${GIT_BRANCH}-${GITHUB_SHA:0:7}-$(date +%Y%m%dT%H%M%S); fi && \
    echo "version=$version" && \
    go build -mod=vendor -o /build/lampa-api -ldflags "-X main.revision=${version} -s -w" ./cmd/lampa-api

# Stage 2: Runtime image, init.sh starts as root and runs CMD as the non-root app user
FROM ghcr.io/umputun/baseimage/app:v1.21.1

ARG GITHUB_SHA

LABEL org.opencontainers.image.source="https://github.com/SvetlovA/lampa"
LABEL org.opencontainers.image.revision="${GITHUB_SHA}"
LABEL org.opencontainers.image.description="Lampa user-data persistence service"

COPY --from=build /build/lampa-api /srv/lampa-api
RUN chmod +x /srv/lampa-api

# 5800 user-data API, 8081 container-internal health, never published (Svtlv convention)
EXPOSE 5800 8081

WORKDIR /srv

CMD ["/srv/lampa-api"]
