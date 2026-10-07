#!/usr/bin/env bash
# Dump the database to a compressed file and prove it restores.
#   scripts/backup.sh                      back up $DATABASE_URL into ./backups
#   scripts/backup.sh restore FILE URL     restore FILE into the (empty) database at URL
#   scripts/backup.sh verify               back up, restore into a scratch database, compare row counts
set -euo pipefail
cmd="${1:-backup}"
dir="${BACKUP_DIR:-backups}"
url="${DATABASE_URL:-postgres://goat:goat@localhost:5432/goat?sslmode=disable}"
# Use the postgres container when pg_dump is not installed locally.
run() { if command -v "$1" >/dev/null; then "$@"; else docker compose exec -T postgres "$@"; fi; }

backup() {
  mkdir -p "$dir"
  out="$dir/goat-$(date +%Y%m%d-%H%M%S).sql.gz"
  run pg_dump --no-owner --clean --if-exists "$url" | gzip > "$out"
  echo "$out"
}

restore() { gunzip -c "$1" | run psql -v ON_ERROR_STOP=1 -q "$2" >/dev/null; }

counts() {
  run psql -At "$1" -c "SELECT (SELECT count(*) FROM catalog.products) || ',' || (SELECT count(*) FROM identity.users) || ',' || (SELECT count(*) FROM orders.orders)"
}

case "$cmd" in
  backup) backup ;;
  restore) restore "${2:?backup file}" "${3:?target database url}" ;;
  verify)
    f="$(backup)"
    admin="${url%/*}/postgres?sslmode=disable"
    scratch="goat_restore_check"
    run psql "$admin" -qc "DROP DATABASE IF EXISTS $scratch WITH (FORCE)" -c "CREATE DATABASE $scratch"
    restore "$f" "${url%/*}/$scratch?sslmode=disable"
    a="$(counts "$url")"; b="$(counts "${url%/*}/$scratch?sslmode=disable")"
    run psql "$admin" -qc "DROP DATABASE $scratch WITH (FORCE)"
    [ "$a" = "$b" ] && echo "backup $f restores correctly (products,users,orders = $a)" || { echo "MISMATCH: live=$a restored=$b"; exit 1; }
    ;;
  *) echo "usage: $0 [backup|restore FILE URL|verify]"; exit 2 ;;
esac
