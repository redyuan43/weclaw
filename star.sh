#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bin_path="${repo_dir}/weclaw"
state_dir="${HOME}/.weclaw"
pid_file="${state_dir}/weclaw.pid"
log_file="${state_dir}/weclaw.log"

mkdir -p "${state_dir}"

if [[ ! -x "${bin_path}" ]]; then
  echo "missing executable: ${bin_path}" >&2
  exit 1
fi

stop_pid() {
  local pid="$1"
  if [[ -z "${pid}" ]]; then
    return 0
  fi
  if ! kill -0 "${pid}" 2>/dev/null; then
    return 0
  fi

  kill "${pid}" 2>/dev/null || true
  for _ in 1 2 3 4 5; do
    if ! kill -0 "${pid}" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  kill -9 "${pid}" 2>/dev/null || true
}

if [[ -f "${pid_file}" ]]; then
  old_pid="$(tr -d '[:space:]' < "${pid_file}" || true)"
  stop_pid "${old_pid}"
  rm -f "${pid_file}"
fi

while IFS= read -r pid; do
  stop_pid "${pid}"
done < <(ps -eo pid=,args= | awk -v bin="${bin_path}" '$2==bin && $3=="start" && $4=="-f" {print $1}')

setsid -f sh -c "exec \"${bin_path}\" start -f >>\"${log_file}\" 2>&1 </dev/null"
sleep 1
new_pid="$(ps -eo pid=,args= | awk -v bin="${bin_path}" '$2==bin && $3=="start" && $4=="-f" {pid=$1} END {print pid}')"
if [[ -z "${new_pid}" ]]; then
  echo "failed to start ${bin_path}" >&2
  exit 1
fi
echo "${new_pid}" > "${pid_file}"

echo "weclaw started from repo binary"
echo "pid=${new_pid}"
echo "bin=${bin_path}"
echo "log=${log_file}"
