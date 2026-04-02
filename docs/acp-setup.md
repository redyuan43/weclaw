# WeClaw ACP 安装与配置说明

这份文档说明如何在新设备上为 `WeClaw` 配置 ACP Agent，并给出当前已经验证过的推荐写法。

目标：

- 让 `WeClaw` 通过 ACP 长驻连接 Agent，而不是每条消息都起一个新进程
- 在微信场景下获得更稳定的多轮上下文
- 明确哪些 Agent 已验证可用，哪些还需要额外安装

## 1. 结论先看

当前已经验证：

- `codex`：推荐使用 ACP
  - 方式：直接使用 `codex app-server --listen stdio://`
  - 优点：支持会话/线程复用，适合微信多轮对话
- `opencode`：支持 ACP
  - 方式：`opencode acp`
- `qwen`：支持 ACP
  - 方式：`qwen --acp`

当前未在这台机器上验证出可直接使用的 ACP 入口：

- `claude`
  - 这台机器上只有 `claude` CLI
  - 未找到现成可执行文件 `claude-agent-acp`
  - `claude` CLI 当前也没有像 `codex` 一样可直接替代的 `app-server` ACP 入口

所以在新设备上，推荐优先把 `codex` 配成 ACP；`claude` 只有在明确安装了可用的 ACP 入口后再切换。

## 2. 为什么优先用 ACP

`WeClaw` 支持三种 Agent 接入模式：

- `acp`：长驻子进程，通过 stdio JSON-RPC 通信
- `cli`：每条消息启动一个新进程
- `http`：OpenAI 兼容接口

在微信场景里，ACP 的主要优势是：

- 进程不需要每条消息重复启动
- 会话可以复用
- 多轮上下文更稳定
- 富媒体场景下更容易保持同一条线程内的连续对话

## 3. 预检查

先确认本机有哪些 Agent 能力：

```bash
command -v codex
codex app-server --help

command -v opencode
opencode acp --help

command -v qwen
qwen --acp --help

command -v claude
command -v claude-agent-acp
claude --help
```

判断原则：

- 命令存在，且对应 `--help` 能正常返回：可以继续配
- `command -v claude-agent-acp` 为空：不要把 `claude` 直接改成 `acp`

## 4. 推荐配置

配置文件路径：

```bash
~/.weclaw/config.json
```

说明：

- 文档示例里可以用 `~`
- 但如果某些运行环境不会自动展开 `~`，请改成绝对路径
- 最稳妥的方式仍然是实际运行配置使用绝对路径

推荐示例：

```json
{
  "default_agent": "codex",
  "media_service_url": "http://127.0.0.1:5010/api/media",
  "agents": {
    "claude": {
      "type": "cli",
      "command": "~/.nvm/versions/node/v22.20.0/bin/claude",
      "model": "sonnet"
    },
    "codex": {
      "type": "acp",
      "command": "~/.nvm/versions/node/v22.20.0/bin/codex",
      "args": [
        "app-server",
        "--listen",
        "stdio://",
        "-c",
        "model_reasoning_effort=\"high\""
      ],
      "cwd": "~/github/weclaw",
      "model": "gpt-5.1-codex-mini"
    },
    "opencode": {
      "type": "acp",
      "command": "~/.opencode/bin/opencode",
      "args": [
        "acp"
      ]
    },
    "qwen": {
      "type": "acp",
      "command": "~/.nvm/versions/node/v22.20.0/bin/qwen",
      "args": [
        "--acp"
      ]
    }
  }
}
```

## 5. 每个 Agent 的配置方式

### 5.1 Codex

推荐配置：

```json
{
  "type": "acp",
  "command": "~/.nvm/versions/node/v22.20.0/bin/codex",
  "args": [
    "app-server",
    "--listen",
    "stdio://",
    "-c",
    "model_reasoning_effort=\"high\""
  ],
  "cwd": "~/github/weclaw",
  "model": "gpt-5.1-codex-mini"
}
```

说明：

- `app-server --listen stdio://` 是 ACP 入口
- `model_reasoning_effort="high"` 用于避免某些模型在默认更高推理强度下报错

验证成功日志应包含：

```text
[acp] sending initialize handshake (..., protocol=codex_app_server)
[agent] started ACP agent: codex
```

### 5.2 OpenCode

推荐配置：

```json
{
  "type": "acp",
  "command": "~/.opencode/bin/opencode",
  "args": [
    "acp"
  ]
}
```

### 5.3 Qwen

推荐配置：

```json
{
  "type": "acp",
  "command": "~/.nvm/versions/node/v22.20.0/bin/qwen",
  "args": [
    "--acp"
  ]
}
```

### 5.4 Claude

当前建议：

- 如果机器上没有 `claude-agent-acp`，先保持 `cli`
- 不要因为 `claude` 已安装就直接把 `type` 改成 `acp`

当前可保守使用：

```json
{
  "type": "cli",
  "command": "~/.nvm/versions/node/v22.20.0/bin/claude",
  "model": "sonnet"
}
```

如果未来某台机器已经具备 `claude-agent-acp`，再改成：

```json
{
  "type": "acp",
  "command": "/path/to/claude-agent-acp",
  "model": "sonnet"
}
```

前提是先验证：

```bash
command -v claude-agent-acp
claude-agent-acp --help
```

## 6. 启动顺序

如果启用了富媒体服务，推荐启动顺序：

```bash
cd ~/github/PyWxDump
./start.sh
```

然后：

```bash
cd ~/github/weclaw
weclaw start
```

## 7. 验证方法

### 7.1 健康检查

```bash
curl http://127.0.0.1:5010/api/media/health
curl http://127.0.0.1:18011/health
```

### 7.2 日志检查

```bash
tail -f ~/.weclaw/weclaw.log
```

重点看这些关键字：

- `Media service enabled`
- `started ACP agent`
- `default agent ready`
- `dispatching to agent`
- `agent replied`

如果是 `codex` ACP，还要看到：

- `protocol=codex_app_server`

### 7.3 交互回归

建议按这个顺序测试：

1. 文本：`你好，你在吗`
2. 多轮文本：连续问两个相关问题，确认上下文还在
3. 语音：说一句短中文
4. 图片 + 一句说明
5. 文件 + 一句说明

## 8. 常见问题

### 8.1 改成 ACP 后没有生效

先看 `~/.weclaw/weclaw.log`：

- 如果仍然是 `created CLI agent`
  - 说明当前运行的不是你刚修改后的配置
  - 或者旧进程还没停干净

处理：

```bash
weclaw stop
weclaw start
```

必要时确认只有一份 `weclaw` 在跑。

### 8.2 `claude` 切成 ACP 后起不来

通常原因：

- 本机没有 `claude-agent-acp`
- 误把普通 `claude` CLI 当成 ACP 可执行文件

处理：

- 先切回 `cli`
- 明确拿到 `claude-agent-acp` 的安装来源后再改

### 8.3 图片发了，后面补一句语音，Agent 接不上

这不完全是 ACP 安装问题。

ACP 能解决的是：

- Agent 线程上下文复用

但如果图片和后续语音在接入层被当成两次独立请求，仍然可能需要额外做“最近富媒体上下文拼接”。

也就是说：

- ACP 是必要增强
- 但不一定单独足以覆盖“先发图，再补一句说明”的所有交互习惯

## 9. 推荐实践

在新设备上，建议按下面顺序配置：

1. 先把 `codex` 配成 ACP 并验证通过
2. 再接 `PyWxDump` 富媒体服务
3. 再验证微信里的图片、语音、文件场景
4. `claude` 只有在确认具备 ACP 可执行文件时再切换

如果目标是“先发图，再补一句语音说明”这类微信原生用法，仅有 ACP 还不一定够，后续最好继续补一层富媒体上下文拼接逻辑。
