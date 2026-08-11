FROM rust:1.86-bookworm@sha256:300ec56abce8cc9448ddea2172747d048ed902a3090e6b57babb2bf19f754081 AS addrgen-rust

ARG JUNO_ADDRGEN_REPO=https://github.com/junocash-tools/juno-addrgen.git
ARG JUNO_ADDRGEN_REF=4a2b3a361c7c1cc3e15891b0befb2eb3dfddb834

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone "${JUNO_ADDRGEN_REPO}" . && git checkout --detach "${JUNO_ADDRGEN_REF}"
RUN cargo build --locked --release --manifest-path rust/addrgen/Cargo.toml

FROM golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac AS addrgen-go

RUN apt-get update && apt-get install -y --no-install-recommends build-essential \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY --from=addrgen-rust /src /src
RUN go build -trimpath -ldflags "-s -w" -o /out/juno-addrgen ./cmd/juno-addrgen

FROM rust:1.86-bookworm@sha256:300ec56abce8cc9448ddea2172747d048ed902a3090e6b57babb2bf19f754081 AS txbuild-rust

ARG JUNO_TXBUILD_REPO=https://github.com/junocash-tools/juno-txbuild.git
ARG JUNO_TXBUILD_REF=05fb761c92ccb9a5da1cafec1e56c3cdca7ca20a

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone "${JUNO_TXBUILD_REPO}" . && git checkout --detach "${JUNO_TXBUILD_REF}"
RUN cargo build --locked --release --manifest-path rust/txbuild/Cargo.toml

FROM golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac AS txbuild-go

RUN apt-get update && apt-get install -y --no-install-recommends build-essential \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY --from=txbuild-rust /src /src
RUN GOWORK=off go mod download
RUN GOWORK=off go build -trimpath -ldflags "-s -w" -o /out/juno-txbuild ./cmd/juno-txbuild

FROM golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac AS gateway-build

ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_TIME
ARG COMPONENTS_JSON={}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=${VERSION} -X main.revision=${REVISION} -X main.buildTime=${BUILD_TIME} -X 'main.componentsJSON=${COMPONENTS_JSON}'" \
  -o /out/juno-exchange-gateway ./cmd/juno-exchange-gateway

FROM debian:bookworm-slim@sha256:abd67ffcfa541b485a3dff59865ab629aa048a6c613e639d36e7456b0b229241

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
  && rm -rf /var/lib/apt/lists/* \
  && groupadd --system --gid 10001 juno \
  && useradd --system --uid 10001 --gid 10001 --no-log-init --home-dir /var/lib/juno-gateway juno \
  && install -d -o 10001 -g 10001 -m 0750 /var/lib/juno-gateway \
  && install -d -o 10001 -g 10001 -m 0700 /var/lib/juno-installation \
  && install -d -o 10001 -g 10001 -m 0700 /run/juno-signer

COPY --from=gateway-build /out/juno-exchange-gateway /usr/local/bin/juno-exchange-gateway
COPY --from=addrgen-go /out/juno-addrgen /usr/local/bin/juno-addrgen
COPY --from=addrgen-rust /src/rust/addrgen/target/release/libjuno_addrgen.so /usr/local/lib/libjuno_addrgen.so
COPY --from=txbuild-go /out/juno-txbuild /usr/local/bin/juno-txbuild
COPY --from=txbuild-rust /src/rust/txbuild/target/release/libjuno_txbuild.so /usr/local/lib/libjuno_txbuild.so
RUN ldconfig

ENV LD_LIBRARY_PATH=/usr/local/lib
USER 10001:10001
EXPOSE 8080 8081
ENTRYPOINT ["juno-exchange-gateway"]
