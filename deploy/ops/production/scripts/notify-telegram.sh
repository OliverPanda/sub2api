#!/usr/bin/env bash
set -Eeuo pipefail

status=${1:-unknown}
version=${2:-unknown}
digest=${3:-unknown}
run_url=${4:-}

if [[ -z "${TELEGRAM_BOT_TOKEN:-}" || -z "${TELEGRAM_CHAT_ID:-}" ]]; then
  printf 'Telegram credentials are not configured; skipping notification.\n' >&2
  exit 0
fi

case "$status" in
  success) icon="✅"; title="生产发布成功" ;;
  skipped) icon="⚪"; title="生产发布已跳过" ;;
  cancelled) icon="⚪"; title="生产发布已取消" ;;
  *) icon="❌"; title="生产发布失败" ;;
esac

message="${icon} Sub2API ${title}"
message+=$'\n\n'"版本: ${version}"
message+=$'\n'"镜像: ${digest}"
if [[ -n "$run_url" ]]; then
  message+=$'\n'"运行记录: ${run_url}"
fi
message+=$'\n'"时间: $(date -u +'%Y-%m-%d %H:%M:%S UTC')"

payload=$(jq -n \
  --arg chat_id "$TELEGRAM_CHAT_ID" \
  --arg text "$message" \
  '{chat_id: $chat_id, text: $text, disable_web_page_preview: true}')

curl --silent --show-error --fail-with-body \
  --connect-timeout 10 --max-time 30 \
  -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
  -H 'Content-Type: application/json' \
  --data "$payload" >/dev/null
