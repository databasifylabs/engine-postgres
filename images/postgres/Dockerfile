# check=skip=InvalidDefaultArgInFrom
ARG GO_IMAGE
ARG BASE_IMAGE
ARG ETCD_IMAGE

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS wrapper
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/engine-postgres
COPY --from=modules . /src/
COPY go.mod go.sum* ./
COPY cmd ./cmd
COPY engine ./engine
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/databasify ./cmd/databasify

FROM ${ETCD_IMAGE} AS etcd

FROM ${BASE_IMAGE}
ARG RELEASE
ARG SDK_VERSION
ARG LINE
ARG PACKAGES
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends $PACKAGES; \
    rm -rf /var/lib/apt/lists/* /usr/local/bin/gosu
COPY --from=etcd /usr/local/bin/etcd /usr/local/bin/etcdctl /usr/local/bin/
RUN set -eux; \
    groupmod -g 64999 postgres; \
    usermod -u 64999 -g 64999 postgres; \
    find / -xdev \( -uid 999 -o -gid 999 \) -exec chown -h 64999:64999 {} +
COPY --from=wrapper /out/databasify /usr/local/bin/databasify
LABEL org.opencontainers.image.source="https://github.com/databasifylabs/engine-postgres" \
      io.databasify.engine="postgres" \
      io.databasify.release="${RELEASE}" \
      io.databasify.line="${LINE}" \
      io.databasify.sdk="${SDK_VERSION}"
ENV DATABASIFY_ENGINE=postgres \
    DATABASIFY_RELEASE=${RELEASE} \
    DATABASIFY_LINE=${LINE}
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/databasify", "serve"]
CMD []
