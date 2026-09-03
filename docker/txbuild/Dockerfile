FROM rust:1.86-bookworm@sha256:300ec56abce8cc9448ddea2172747d048ed902a3090e6b57babb2bf19f754081 AS rust-build

ARG JUNO_TXBUILD_REPO=https://github.com/junocash-tools/juno-txbuild.git
ARG JUNO_TXBUILD_REF=553a977038c7f0660fc361acc059df80ce3101f3

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone "${JUNO_TXBUILD_REPO}" . && git checkout --detach "${JUNO_TXBUILD_REF}"
RUN cargo build --locked --release --manifest-path rust/txbuild/Cargo.toml

FROM golang:1.27-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS go-build

RUN apt-get update && apt-get install -y --no-install-recommends build-essential \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY --from=rust-build /src /src
RUN GOWORK=off go mod download
RUN GOWORK=off go build -trimpath -ldflags "-s -w" -o /out/juno-txbuild ./cmd/juno-txbuild

FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
  && rm -rf /var/lib/apt/lists/* \
  && groupadd --system juno \
  && useradd --system --gid juno --create-home juno
COPY --from=go-build /out/juno-txbuild /usr/local/bin/juno-txbuild
COPY --from=rust-build /src/rust/txbuild/target/release/libjuno_txbuild.so /usr/local/lib/libjuno_txbuild.so
RUN ldconfig
ENV LD_LIBRARY_PATH=/usr/local/lib
USER juno
WORKDIR /home/juno
ENTRYPOINT ["juno-txbuild"]
