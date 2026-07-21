---
title: Docker operations
---

The default appliance runs `junocashd`, `juno-scan`, and the gateway. Only the gateway port is published.

## Storage modes

RocksDB is the default single-host scanner store:

```bash
docker compose up -d
```

Postgres is recommended for production:

```bash
docker compose -f compose.yaml -f compose.postgres.yaml up -d
```

The Postgres override creates a private database service and persistent volume. MySQL is not a supported appliance mode.

## Local builds

```bash
docker compose -f compose.yaml -f compose.dev.yaml build
docker compose up -d
```

Production deployments should set every `JUNO_*_IMAGE` to an immutable digest before `docker compose up -d`.

## Routine commands

```bash
docker compose ps
docker compose logs --since=15m gateway juno-scan junocashd
docker compose restart gateway
docker compose down
```

`docker compose down` keeps named volumes. Do not add `--volumes` during routine operations.

The `txbuild` service runs only on demand:

```bash
docker compose --profile operator run --rm txbuild --help
```

## Exposure

The default bind is `127.0.0.1:8080`. To serve remote traffic, place a TLS or mTLS reverse proxy in front and then set `JUNO_GATEWAY_BIND=0.0.0.0`. Never publish node RPC, ZMQ, scanner, or Postgres ports.
