FROM ubuntu:25.10@sha256:7cc5e35f6567ee8c66d2abb4aab0fd866669e6207c237c3a8f0947a5c7f17092

ARG JUNOCASH_VERSION=0.9.13
ARG JUNOCASH_LINUX64_SHA256=7e83b8c888344f33129ece2160d8b6434bc91ffb9cd992665aadd2c02c904f11
ARG TARGETARCH

RUN test "$TARGETARCH" = "amd64"

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    libnuma1 \
    tar \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /tmp
RUN curl --retry 5 --retry-all-errors --retry-delay 2 -fsSLo "junocash-${JUNOCASH_VERSION}-linux64.tar.gz" \
      "https://github.com/juno-cash/junocash/releases/download/v${JUNOCASH_VERSION}/junocash-${JUNOCASH_VERSION}-linux64.tar.gz" \
 && echo "${JUNOCASH_LINUX64_SHA256}  junocash-${JUNOCASH_VERSION}-linux64.tar.gz" | sha256sum -c - \
 && mkdir -p /tmp/junocash \
 && tar -xzf "junocash-${JUNOCASH_VERSION}-linux64.tar.gz" -C /tmp/junocash --strip-components=1 \
 && install -m 0755 "/tmp/junocash/bin/junocashd" /usr/local/bin/junocashd \
 && install -m 0755 "/tmp/junocash/bin/junocash-cli" /usr/local/bin/junocash-cli \
 && rm -rf /tmp/*

RUN groupadd --system juno \
 && useradd --system --gid juno --home-dir /data juno \
 && install -d -o juno -g juno -m 0700 /data

COPY docker/junocashd/entrypoint.sh /usr/local/bin/junocashd-entrypoint
COPY docker/junocashd/juno-cli /usr/local/bin/juno-cli
RUN chmod 0755 /usr/local/bin/junocashd-entrypoint /usr/local/bin/juno-cli

VOLUME ["/data"]
USER juno
ENTRYPOINT ["junocashd-entrypoint"]
