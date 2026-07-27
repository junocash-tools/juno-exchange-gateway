#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  exec /usr/local/bin/juno-txsign "$@"
fi

if [ "${1:-}" = "serve-txplan" ]; then
  copy_private_file() {
    source_path="$1"
    target_path="$2"
    label="$3"

    if [ ! -f "$source_path" ] || [ -L "$source_path" ]; then
      echo "txsign container: $label source must be a regular non-symlink file" >&2
      exit 1
    fi
    case "$(stat -c '%a' "$source_path")" in
      400|600) ;;
      *)
        echo "txsign container: $label source must have mode 0400 or 0600" >&2
        exit 1
        ;;
    esac
    install -m 0600 "$source_path" "$target_path"
    chown 10001:10001 "$target_path"
    if [ "$(stat -c '%u:%g:%a' "$target_path")" != "10001:10001:600" ]; then
      echo "txsign container: could not protect the private $label copy" >&2
      exit 1
    fi
  }

  install -d -o 10001 -g 10001 -m 0700 /run/juno-txsign-secrets
  copy_private_file /run/host-secrets/seed.b64 /run/juno-txsign-secrets/seed.b64 seed
  copy_private_file /run/host-secrets/bindings.json /run/juno-txsign-secrets/bindings.json bindings
fi

exec setpriv \
  --reuid 10001 \
  --regid 10001 \
  --clear-groups \
  --no-new-privs \
  --bounding-set=-all \
  --inh-caps=-all \
  --ambient-caps=-all \
  /usr/local/bin/juno-txsign "$@"
