FROM rust:1.86-bookworm@sha256:300ec56abce8cc9448ddea2172747d048ed902a3090e6b57babb2bf19f754081 AS rust-build

ARG JUNO_TXSIGN_REPO=https://github.com/junocash-tools/juno-txsign.git
ARG JUNO_TXSIGN_REF=d33f925dc73ee929ac95073fdcd40a4f17dbb43d

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone "${JUNO_TXSIGN_REPO}" . && git checkout --detach "${JUNO_TXSIGN_REF}"
RUN cargo build --locked --release --manifest-path rust/juno-tx/Cargo.toml \
  && cargo build --locked --release --manifest-path rust/witness/Cargo.toml

FROM golang:1.27-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS go-build

RUN apt-get update && apt-get install -y --no-install-recommends build-essential \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY --from=rust-build /src /src
RUN GOWORK=off go mod download
RUN GOWORK=off go build -trimpath -ldflags "-s -w" -o /out/juno-txsign ./cmd/juno-txsign

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl util-linux \
  && rm -rf /var/lib/apt/lists/* \
  && groupadd --system --gid 10001 juno \
  && useradd --system --uid 10001 --gid 10001 --no-log-init --home-dir /var/lib/juno-txsign juno \
  && install -d -o 10001 -g 10001 -m 0700 /run/juno-signer \
  && install -d -o 10001 -g 10001 -m 0700 /run/juno-txsign-secrets \
  && install -d -o 0 -g 0 -m 0700 /run/host-secrets \
  && install -d -o 10001 -g 10001 -m 0700 /var/lib/juno-txsign/results

COPY --from=go-build /out/juno-txsign /usr/local/bin/juno-txsign
COPY --from=rust-build /src/rust/juno-tx/target/release/libjuno_tx.so /usr/local/lib/libjuno_tx.so
COPY --from=rust-build /src/rust/witness/target/release/libjuno_tx_witness.so /usr/local/lib/libjuno_tx_witness.so
COPY --chmod=0755 docker/txsign/entrypoint.sh /usr/local/bin/juno-txsign-container-entrypoint
RUN ldconfig
ENV LD_LIBRARY_PATH=/usr/local/lib
WORKDIR /var/lib/juno-txsign
USER 10001:10001
ENTRYPOINT ["juno-txsign-container-entrypoint"]
