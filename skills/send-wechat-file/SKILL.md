---
name: send-wechat-file
description: Send a local file, photo, screenshot, PDF, video, or other document to a WeChat user through a running WeClaw/ClawBot instance. Use when the user asks from the command line or Codex to push a specified local path to their WeChat/ClawBot contact, wants a simple reusable script for sending files, or asks how to send generated session artifacts such as images and PDFs via WeClaw.
---

# Send WeChat File

## Quick Start

Use the repository helper first when working inside the WeClaw repo:

```bash
./scripts/send-wechat-file.sh --to "USER_ID@im.wechat" --text "optional note" "/absolute/path/to/file.pdf"
```

If the helper is not available, use the bundled script in this skill:

```bash
skills/send-wechat-file/scripts/send-wechat-file.sh --to "USER_ID@im.wechat" "/absolute/path/to/file.png"
```

Use `WECLAW_TO` when the user wants a shorter command:

```bash
export WECLAW_TO="USER_ID@im.wechat"
./scripts/send-wechat-file.sh "/absolute/path/to/file.pdf"
```

## Workflow

1. Confirm WeClaw is running:

```bash
curl -fsS "${WECLAW_API_URL:-http://127.0.0.1:18011}/health"
```

2. Confirm the target file exists and is a regular file:

```bash
test -f "/absolute/path/to/file"
```

3. Send by local API path, not by uploading the file anywhere yourself:

```bash
curl -X POST "${WECLAW_API_URL:-http://127.0.0.1:18011}/api/send" \
  -H "Content-Type: application/json" \
  -d '{"to":"USER_ID@im.wechat","media_path":"/absolute/path/to/file.pdf"}'
```

4. If the user wants a reusable command, prefer the helper script over writing a new one.

## Choosing the Recipient

- If `WECLAW_TO` is already set, use it.
- If the user gives a WeChat/iLink user ID, pass it as `--to`.
- If the user says "send to me" but no ID is known, look for recent inbound IDs in WeClaw logs or inbox data before asking:

```bash
tail -n 200 ~/.weclaw/weclaw.log | rg "@im\\.wechat"
```

Ask only if no clear target user ID can be found.

## Notes

- The API must be local and running, usually `http://127.0.0.1:18011`.
- `media_path` must be readable by the running WeClaw process.
- Supported outgoing classification is handled by WeClaw: images are sent as images, videos as videos, and other files as file cards.
- For generated session artifacts, send the final artifact path directly; do not copy it into a web server or convert it to a URL.
