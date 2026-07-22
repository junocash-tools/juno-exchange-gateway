FROM rust:1.86-bookworm@sha256:300ec56abce8cc9448ddea2172747d048ed902a3090e6b57babb2bf19f754081 AS rust-build

ARG JUNO_TXSIGN_REPO=https://github.com/junocash-tools/juno-txsign.git
ARG JUNO_TXSIGN_REF=4459cd827062c8d317970e48e7ed5e2e4d0f5c6b

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone "${JUNO_TXSIGN_REPO}" . && git checkout --detach "${JUNO_TXSIGN_REF}"
RUN cargo build --locked --release --manifest-path rust/juno-tx/Cargo.toml \
  && cargo build --locked --release --manifest-path rust/witness/Cargo.toml

FROM golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac AS go-build

RUN apt-get update && apt-get install -y --no-install-recommends build-essential \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY --from=rust-build /src /src
RUN GOWORK=off go mod download
RUN GOWORK=off go build -trimpath -ldflags "-s -w" -o /out/juno-txsign ./cmd/juno-txsign

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

COPY --from=go-build /out/juno-txsign /usr/local/bin/juno-txsign
COPY --from=rust-build /src/rust/juno-tx/target/release/libjuno_tx.so /usr/local/lib/libjuno_tx.so
COPY --from=rust-build /src/rust/witness/target/release/libjuno_tx_witness.so /usr/local/lib/libjuno_tx_witness.so
ENV LD_LIBRARY_PATH=/usr/local/lib
WORKDIR /work
USER 65532:65532
ENTRYPOINT ["juno-txsign"]
