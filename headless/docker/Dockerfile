FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG BUILD_VERSION=0.4.0-alpha.1
ARG BUILD_COMMIT=unknown
ARG BUILD_TIME=unknown
ENV CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH
WORKDIR /src

COPY relay /src/relay
COPY headless /src/headless

RUN LDFLAGS="-s -w -X whitelist-bypass/relay/common.Version=${BUILD_VERSION} -X whitelist-bypass/relay/common.BuildCommit=${BUILD_COMMIT} -X whitelist-bypass/relay/common.BuildTime=${BUILD_TIME}" \
 && go -C /src/headless/vk        build -trimpath -ldflags="$LDFLAGS" -o /out/headless-vk-creator . \
 && go -C /src/headless/telemost  build -trimpath -ldflags="$LDFLAGS" -o /out/headless-telemost-creator . \
 && go -C /src/headless/wbstream  build -trimpath -ldflags="$LDFLAGS" -o /out/headless-wbstream-creator . \
 && go -C /src/headless/dion      build -trimpath -ldflags="$LDFLAGS" -o /out/headless-dion-creator . \
 && go -C /src/headless/vk-bot    build -trimpath -ldflags="$LDFLAGS" -o /out/headless-vk-bot .

FROM debian:bookworm-slim
LABEL org.opencontainers.image.licenses="MIT" \
      io.github.whitelist-bypass.upstream="https://github.com/kulikov0/whitelist-bypass" \
      io.github.whitelist-bypass.upstream-revision="64aa77acd5b52c34f5ddbd1ad0d861ea65bc8943"

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tini \
 && rm -rf /var/lib/apt/lists/*

RUN useradd --system --create-home --home /home/wlb --shell /usr/sbin/nologin wlb \
 && mkdir -p /opt/wlb/bin /data/sessions \
 && chown -R wlb:wlb /data

COPY --from=build /out/headless-vk-creator        /opt/wlb/bin/
COPY --from=build /out/headless-telemost-creator  /opt/wlb/bin/
COPY --from=build /out/headless-wbstream-creator  /opt/wlb/bin/
COPY --from=build /out/headless-dion-creator      /opt/wlb/bin/
COPY --from=build /out/headless-vk-bot            /usr/local/bin/

COPY LICENSE NOTICE /usr/share/licenses/whitelist-bypass/
COPY headless/docker/entrypoint.sh /usr/local/bin/wlb-entrypoint
COPY headless/docker/direct-entrypoint.sh /usr/local/bin/wlb-direct-entrypoint
RUN chmod +x /usr/local/bin/wlb-entrypoint /usr/local/bin/wlb-direct-entrypoint

USER wlb
WORKDIR /data
VOLUME ["/data"]

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/wlb-entrypoint"]
