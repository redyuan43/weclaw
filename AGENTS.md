# WeClaw 工作约定

## 修改后验证与重启

- 每次修改 WeClaw 代码后，都要重新格式化、测试、构建并重启正在运行的 WeClaw。
- 推荐流程：
  1. `gofmt -w <changed-go-files>`
  2. `go test ./...`
  3. `go build -o ./weclaw .`
  4. `./start.sh`
- 如果当前环境没有系统级 `go` / `gofmt`，优先使用本仓库已验证过的用户级 Go 工具链路径：`~/.cache/weclaw-go-toolchain/go/bin/go` 和 `~/.cache/weclaw-go-toolchain/go/bin/gofmt`。
- 重启后要确认进程和本地 API 健康状态，例如检查 `~/.weclaw/weclaw.pid`、`ps`、`~/.weclaw/weclaw.log`，以及 `curl http://127.0.0.1:18011/health`。
- 如果健康检查失败，不要只报告“已重启”；必须继续查看日志并定位启动失败原因。

## 微信长任务与 2 分钟超时

- 微信端出现 `处理超时（超过 2m0s），请缩小问题范围后重试。` 时，含义是 WeClaw 前台等待 agent 回复的同步窗口超时，不是微信/iLink 发送失败，也不一定代表底层任务已经失败。
- 不要简单把 2 分钟同步等待时间调得很长。iLink `context_token` 很短命，长时间同步等待后再主动回复，容易遇到 `ret=-2`。
- 长任务应该走两段式：
  1. 前台最多等待约 2 分钟；未完成时先回复“任务仍在后台执行，完成后会自动发送结果”。
  2. 后台继续运行任务，默认最长 1 小时；完成后再发最终结果。
- 后台完成后的微信发送必须复用 pending/outbox 兜底：如果发送遇到 `ret=-2`、CDN 5xx、timeout、EOF 等临时错误，应写入 `~/.weclaw/pending-sends/`，等待下一次微信入站刷新 context 后自动补发。
- 排查这类问题时优先看 `messaging/handler.go` 的 agent 等待超时逻辑、`messaging/pending_outgoing.go` 的补发队列，以及 `~/.weclaw/weclaw.log` 里是否有 `context deadline exceeded`、`ret=-2`、`queued pending send`。
