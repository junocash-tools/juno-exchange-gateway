FROM ubuntu:26.04@sha256:3131b4cc82a783df6c9df078f86e01819a13594b865c2cad47bd1bca2b7063bb

ARG JUNOCASH_VERSION=0.9.12
ARG JUNOCASH_LINUX64_SHA256=41f74d61feb9a4284a4200b1fc163ecd072da3314a25db7222522f2e548ec386
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
 && tar -xzf "junocash-${JUNOCASH_VERSION}-linux64.tar.gz" \
 && install -m 0755 "junocash-${JUNOCASH_VERSION}/bin/junocashd" /usr/local/bin/junocashd \
 && install -m 0755 "junocash-${JUNOCASH_VERSION}/bin/junocash-cli" /usr/local/bin/junocash-cli \
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
