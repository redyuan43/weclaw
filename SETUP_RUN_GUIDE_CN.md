# WeClaw 安装与运行指南

这份文档面向当前这台机器上的源码运行方式，覆盖两种场景：

1. 只运行 `WeClaw`
2. 运行 `WeClaw` + `PyWxDump` 富媒体预处理服务

如果你想让微信里的图片、语音、视频、文件先经过 `PyWxDump` 做结构化处理，再交给 Codex 分析，按第二种方式启动。

## 1. 前置条件

### 1.1 WeClaw

- 系统：Linux
- Go：`1.25.x`
- 已安装并可直接执行的 AI Agent CLI
  - 当前推荐：`codex`
- 已能使用 `weclaw login` 完成微信扫码登录

检查命令：

```bash
go version
codex --version
weclaw version
```

### 1.2 PyWxDump 富媒体服务

如果你要启用富媒体预处理，还需要：

- `~/github/PyWxDump`
- Python 虚拟环境：`~/github/PyWxDump/.venv`
- `ffmpeg` / `ffprobe`

检查命令：

```bash
cd ~/github/PyWxDump
./.venv/bin/python --version
ffmpeg -version
ffprobe -version
```

## 2. 从源码构建 WeClaw

在 `weclaw` 仓库根目录执行：

```bash
cd ~/github/weclaw
go build -ldflags '-X github.com/fastclaw-ai/weclaw/cmd.Version=local-dev' -o /tmp/weclaw-local/weclaw .
sudo install -m 755 /tmp/weclaw-local/weclaw /usr/local/bin/weclaw
weclaw version
```

如果你只是临时试跑，也可以不安装到 `/usr/local/bin`，直接用：

```bash
/tmp/weclaw-local/weclaw start
```

## 3. 配置 WeClaw

配置文件路径：

```bash
~/.weclaw/config.json
```

当前推荐配置示例：

```json
{
  "default_agent": "codex",
  "agents": {
    "codex": {
      "type": "cli",
      "command": "/home/nano/.nvm/versions/node/v24.11.1/bin/codex",
      "model": "gpt-5.1-codex-mini",
      "args": [
        "--skip-git-repo-check",
        "-c",
        "model_reasoning_effort=\"high\""
      ],
      "cwd": "/home/nano/github/weclaw"
    }
  }
}
```

如果要接入 `PyWxDump` 富媒体服务，再加上：

```json
{
  "media_service_url": "http://127.0.0.1:5010/api/media"
}
```

说明：

- `--skip-git-repo-check`：避免 Codex 因目录信任检查直接退出
- `model_reasoning_effort="high"`：避免 `gpt-5.1-codex-mini` 因全局 `xhigh` 配置报错
- `cwd`：指定 Codex 工作目录
- `media_service_url`：启用富媒体预处理服务

## 4. 登录微信账号

首次使用或登录态失效时：

```bash
weclaw login
```

扫码成功后，账号会保存在：

```bash
~/.weclaw/accounts/
```

如果你换过账号，建议确认这里只保留当前有效账号，避免旧账号持续报 `session expired`。

## 5. 启动方式

### 5.1 只运行 WeClaw

```bash
cd ~/github/weclaw
weclaw start
```

检查：

```bash
weclaw status
curl http://127.0.0.1:18011/health
```

### 5.2 运行 WeClaw + PyWxDump 富媒体服务

先启动 `PyWxDump`：

```bash
cd ~/github/PyWxDump
./start.sh
```

当前 `start.sh` 会先确保媒体服务就绪，再进入前台 watcher。你会看到类似输出：

```bash
[*] Media service already healthy: http://127.0.0.1:5010/api/media/health
[2026-.. ..:..:..] 开始事件驱动监听 | ...
```

然后再启动 `WeClaw`：

```bash
cd ~/github/weclaw
weclaw start
```

健康检查：

```bash
curl http://127.0.0.1:5010/api/media/health
curl http://127.0.0.1:18011/health
```

## 6. 日志怎么看

### 6.1 WeClaw

```bash
tail -f ~/.weclaw/weclaw.log
```

看这些关键字：

- `Media service enabled`
- `Starting message bridge`
- `default agent ready`
- `received from`
- `agent replied`

### 6.2 PyWxDump 媒体服务

```bash
tail -f ~/github/PyWxDump/wxdump_work/media-service.log
```

看这些关键字：

- `Uvicorn running on http://127.0.0.1:5010`
- `GET /api/media/health`
- `POST /api/media/process`

### 6.3 PyWxDump watcher

如果你是前台跑 `./start.sh`，直接看当前终端输出即可。

如果 `./start.sh` 提示已有 watcher 在运行，先停掉旧 watcher，再重新执行：

```bash
pkill -f 'linux_wx_chat_daemon.py watch --all-chats --database-only'
cd ~/github/PyWxDump
./start.sh
```

## 7. 回归测试建议

建议按这个顺序测：

1. 文本：`你好，你在吗？`
2. 语音：说一句短中文
3. 图片 + 一句说明：`请描述这张图`
4. 文件 + 一句说明：`请总结这个文件`
5. 视频 + 一句说明：`请说明这个视频的大意`

说明：

- 语音当前是走微信自带转文字，再交给 Codex
- 图片、语音、视频、文件这几类入站消息会优先走 `PyWxDump` 媒体服务
- `WeClaw` 再把结构化结果发给 Codex 生成回复

## 8. 常见问题

### 8.1 `session expired`

表现：

- `~/.weclaw/weclaw.log` 里持续出现 `Run weclaw login to re-authenticate`

处理：

```bash
weclaw stop
weclaw login
weclaw start
```

必要时清理旧账号文件，只保留当前有效账号。

### 8.2 Codex 报 trusted directory 或 git repo 错误

表现：

- `Not inside a trusted directory`
- `--skip-git-repo-check was not specified`

处理：

- 在 `agents.codex.args` 里加 `--skip-git-repo-check`
- 给 `agents.codex.cwd` 配一个明确目录

### 8.3 Codex 报 `xhigh` 不支持

表现：

- `Unsupported value: 'xhigh' is not supported with the 'gpt-5.1-codex-mini' model`

处理：

- 在 `agents.codex.args` 里加入：

```json
["-c", "model_reasoning_effort=\"high\""]
```

### 8.4 `PyWxDump` API 起不来

表现：

- `curl http://127.0.0.1:5010/api/media/health` 不通

先看：

```bash
tail -f ~/github/PyWxDump/wxdump_work/media-service.log
```

再确认：

```bash
cd ~/github/PyWxDump
./start.sh
```

## 9. 当前正式入口

如果你启用富媒体预处理，推荐始终按下面顺序运行：

```bash
cd ~/github/PyWxDump
./start.sh
```

另开一个终端：

```bash
cd ~/github/weclaw
weclaw start
```

这就是当前这套代码的标准运行方式。
