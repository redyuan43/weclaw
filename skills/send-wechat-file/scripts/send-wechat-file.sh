#!/usr/bin/env bash
set -euo pipefail

api_url="${WECLAW_API_URL:-http://127.0.0.1:18011/api/send}"
to="${WECLAW_TO:-}"
text=""

usage() {
  cat >&2 <<'EOF'
Usage:
  send-wechat-file.sh --to USER_ID [--text TEXT] FILE
  WECLAW_TO=USER_ID send-wechat-file.sh [--text TEXT] FILE

Environment:
  WECLAW_API_URL  default: http://127.0.0.1:18011/api/send
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --to)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      to="$2"
      shift 2
      ;;
    --text)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      text="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    -*)
      echo "unknown option: $1" >&2
      usage
      exit 2
      ;;
    *)
      break
      ;;
  esac
done

[[ $# -eq 1 ]] || { usage; exit 2; }
file_path="$1"

if [[ -z "${to}" ]]; then
  echo "missing --to or WECLAW_TO" >&2
  exit 2
fi
if [[ ! -f "${file_path}" ]]; then
  echo "not a file: ${file_path}" >&2
  exit 2
fi

abs_path="$(realpath "${file_path}")"

python3 - "$api_url" "$to" "$abs_path" "$text" <<'PY'
import json
import sys
import urllib.error
import urllib.request

api_url, to, media_path, text = sys.argv[1:5]
payload = {"to": to, "media_path": media_path}
if text:
    payload["text"] = text

data = json.dumps(payload).encode("utf-8")
req = urllib.request.Request(
    api_url,
    data=data,
    headers={"Content-Type": "application/json"},
    method="POST",
)
try:
    with urllib.request.urlopen(req, timeout=120) as resp:
        print(resp.read().decode("utf-8"))
except urllib.error.HTTPError as exc:
    body = exc.read().decode("utf-8", errors="replace")
    raise SystemExit(f"HTTP {exc.code}: {body}")
PY
