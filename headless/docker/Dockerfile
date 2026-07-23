FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG BUILD_VERSION=0.5.0-alpha.12
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
 && go -C /src/headless/vk-bot    build -trimpath -ldflags="$LDFLAGS" -o /out/headless-vk-bot . \
 && go -C /src/headless/manager   test ./... \
 && MANAGER_LDFLAGS="-s -w -X main.Version=${BUILD_VERSION} -X main.BuildCommit=${BUILD_COMMIT} -X main.BuildTime=${BUILD_TIME}" \
 && go -C /src/headless/manager   build -trimpath -ldflags="$MANAGER_LDFLAGS" -o /out/wlb-manager .

FROM debian:bookworm-slim
ARG TARGETARCH
LABEL org.opencontainers.image.licenses="MIT" \
      io.github.whitelist-bypass.upstream="https://github.com/kulikov0/whitelist-bypass" \
      io.github.whitelist-bypass.upstream-revision="64aa77acd5b52c34f5ddbd1ad0d861ea65bc8943"

RUN groupadd --system --gid 999 wlb \
 && useradd --system --uid 999 --gid 999 --create-home --home /home/wlb --shell /usr/sbin/nologin wlb \
 && test "$(id -u wlb)" = "999" \
 && test "$(id -g wlb)" = "999"

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tini \
 && if [ "$TARGETARCH" != "386" ]; then apt-get install -y --no-install-recommends chromium fonts-liberation; fi \
 && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /opt/wlb/bin /data/sessions \
 && chown -R wlb:wlb /data

COPY --from=build /out/headless-vk-creator        /opt/wlb/bin/
COPY --from=build /out/headless-telemost-creator  /opt/wlb/bin/
COPY --from=build /out/headless-wbstream-creator  /opt/wlb/bin/
COPY --from=build /out/headless-dion-creator      /opt/wlb/bin/
COPY --from=build /out/headless-vk-bot            /usr/local/bin/
COPY --from=build /out/wlb-manager                /usr/local/bin/

COPY LICENSE NOTICE /usr/share/licenses/whitelist-bypass/
COPY headless/docker/entrypoint.sh /usr/local/bin/wlb-entrypoint
COPY headless/docker/direct-entrypoint.sh /usr/local/bin/wlb-direct-entrypoint
RUN chmod +x /usr/local/bin/wlb-entrypoint /usr/local/bin/wlb-direct-entrypoint

USER wlb
ENV VK_LOGIN_BROWSER=/usr/bin/chromium
WORKDIR /data
VOLUME ["/data"]

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/wlb-entrypoint"]
