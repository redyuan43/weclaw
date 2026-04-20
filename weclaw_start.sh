#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bin_path="${repo_dir}/weclaw"
start_script="${repo_dir}/start.sh"
accounts_dir="${WECLAW_ACCOUNTS_DIR:-${HOME}/.weclaw/accounts}"
project_dir="$(pwd -P)"

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  cat <<'EOF'
用法:
  weclaw_start.sh

行为:
  1. 如果未绑定微信，先执行 weclaw login 显示二维码扫码
  2. 按当前目录筛选 Codex 自己的 session，并选择要恢复的上下文
  3. 启动 weclaw 后台服务
EOF
  exit 0
fi

if [[ ! -x "${bin_path}" ]]; then
  echo "未找到可执行文件: ${bin_path}" >&2
  exit 1
fi

if [[ ! -x "${start_script}" ]]; then
  echo "未找到启动脚本: ${start_script}" >&2
  exit 1
fi

has_account=0
if [[ -d "${accounts_dir}" ]] && find "${accounts_dir}" -maxdepth 1 -type f -name "*.json" | grep -q .; then
  has_account=1
fi

if [[ ${has_account} -eq 0 ]]; then
  echo "未检测到已绑定的微信账号，先进入扫码登录流程..."
  "${bin_path}" login
fi

echo "当前项目目录: ${project_dir}"
echo "正在检查当前目录对应的 Codex session..."
"${bin_path}" bind-codex-session --cwd "${project_dir}"

echo "正在启动 weclaw 后台服务..."
"${start_script}"
