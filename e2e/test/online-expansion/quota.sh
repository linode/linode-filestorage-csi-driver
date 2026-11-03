#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  printf 'usage: bash quota.sh SPACE_ID/FILESYSTEM_ID EXPECTED_BYTES\n' >&2
  exit 2
fi
volume_handle=$1
expected_bytes=$2
if [[ ! "$volume_handle" =~ ^[1-9][0-9]*/[1-9][0-9]*$ ]]; then
  printf 'volume handle must have format SPACE_ID/FILESYSTEM_ID\n' >&2
  exit 2
fi
if [[ ! "$expected_bytes" =~ ^[1-9][0-9]*$ ]]; then
  printf 'expected quota must be a positive byte count\n' >&2
  exit 2
fi
space_id=${volume_handle%%/*}
filesystem_id=${volume_handle#*/}
: "${LINODE_URL:?Set LINODE_URL to the selected HTTPS API base including its version path}"
: "${LINODE_TOKEN:?Set LINODE_TOKEN through the selected account environment}"

api_base=${LINODE_URL%/}
case "$LINODE_TOKEN" in
  *$'\r'*|*$'\n'*)
    printf 'LINODE_TOKEN must not contain line breaks\n' >&2
    exit 2
    ;;
esac
# Send the secret to curl through its config stdin, not argv or logs.
token_config=${LINODE_TOKEN//\\/\\\\}
token_config=${token_config//\"/\\\"}
unset LINODE_TOKEN

quota_url="${api_base}/nfs/spaces/${space_id}/filesystems/${filesystem_id}/quotas"
curl_options=(--fail --silent --show-error --proto '=https' --connect-timeout 10 --max-time 15)
if [[ -n "${LINODE_CA:-}" ]]; then
  curl_options+=(--cacert "$LINODE_CA")
fi
last_capacity=unknown
for ((attempt = 0; attempt < 150; attempt++)); do
  response=$(printf 'header = "Authorization: Bearer %s"\n' "$token_config" |
    curl "${curl_options[@]}" --config - "$quota_url")
  quota_fields=$(printf '%s\n' "$response" |
    yq -p=json -o=tsv -e '
      [.data[] | select(.is_root == true)]
      | select(length == 1)
      | .[0]
      | [[.filesystem_id, .is_root, .max_capacity_bytes]]
    ')
  IFS=$'\t' read -r root_filesystem_id is_root last_capacity <<< "$quota_fields"
  if [[ ! "$root_filesystem_id" =~ ^[0-9]+$ || "$is_root" != true || ! "$last_capacity" =~ ^[0-9]+$ ]]; then
    printf 'root quota response has invalid filesystem_id, is_root, or max_capacity_bytes\n' >&2
    exit 1
  fi
  if [[ "$root_filesystem_id" != "$filesystem_id" ]]; then
    printf 'root quota belongs to filesystem %s, expected %s\n' "$root_filesystem_id" "$filesystem_id" >&2
    exit 1
  fi
  if [[ "$last_capacity" == "$expected_bytes" ]]; then
    printf '{"filesystem_id":%s,"is_root":true,"max_capacity_bytes":%s}\n' "$root_filesystem_id" "$last_capacity"
    exit 0
  fi
  sleep 2
done
printf 'root quota did not reach %s bytes in 150 polls (last capacity %s)\n' "$expected_bytes" "$last_capacity" >&2
exit 1
