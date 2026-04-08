package api

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/fastclaw-ai/weclaw/controlplane"
)

var consoleTemplates = template.Must(template.New("console").Funcs(template.FuncMap{
	"shortID": func(value string) string {
		if len(value) <= 8 {
			return value
		}
		return value[:8]
	},
	"checked": func(value bool) string {
		if value {
			return "checked"
		}
		return ""
	},
	"selected": func(current, value string) string {
		if current == value {
			return "selected"
		}
		return ""
	},
	"label": func(value string, labels map[string]string) string {
		if display := labels[value]; display != "" {
			return display
		}
		return value
	},
	"joinLines": func(values []string) string {
		return strings.Join(values, "\n")
	},
	"statusClass": func(status string) string {
		switch status {
		case "completed", "approved":
			return "status-ok"
		case "working", "running":
			return "status-running"
		case "pending", "waiting-approval", "blocked":
			return "status-waiting"
		case "rejected":
			return "status-rejected"
		case "failed":
			return "status-failed"
		default:
			return "status-neutral"
		}
	},
	"taskOutput": func(task any) string {
		switch value := task.(type) {
		case controlplane.TaskRecord:
			return firstNonBlank(value.ResultText, value.ErrorText, value.RequestText)
		case *controlplane.TaskRecord:
			if value == nil {
				return ""
			}
			return firstNonBlank(value.ResultText, value.ErrorText, value.RequestText)
		case consoleTaskView:
			return firstNonBlank(value.ResultText, value.ErrorText, value.RequestText)
		case *consoleTaskView:
			if value == nil {
				return ""
			}
			return firstNonBlank(value.ResultText, value.ErrorText, value.RequestText)
		default:
			return ""
		}
	},
}).Parse(`
{{define "layout_start"}}
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <title>{{.Title}}</title>
  <style>
    :root {
      color-scheme: light;
      --bg:#f3ede2;
      --panel:#fffdfa;
      --ink:#172124;
      --muted:#667074;
      --line:#d7c7b4;
      --accent:#c26b42;
      --accent-soft:#f3d8cb;
      --running:#355c7d;
      --ok:#2f7f5f;
      --wait:#b9893a;
      --danger:#a84448;
    }
    * { box-sizing: border-box; }
    body {
      margin:0;
      font-family:"Noto Sans SC","PingFang SC",sans-serif;
      color:var(--ink);
      background:
        radial-gradient(circle at top left, rgba(194,107,66,.18), transparent 24%),
        linear-gradient(180deg, #f7f1e8, #efe5d7 68%);
    }
    header {
      padding:24px 28px 18px;
      border-bottom:1px solid rgba(23,33,36,.08);
      backdrop-filter: blur(10px);
      position: sticky;
      top: 0;
      background: rgba(247,241,232,.92);
      z-index: 20;
    }
    h1 { margin:0; font-size:30px; }
    p, .muted { color:var(--muted); }
    nav {
      display:flex;
      gap:10px;
      margin-top:16px;
      flex-wrap:wrap;
    }
    nav a {
      text-decoration:none;
      color:var(--ink);
      padding:8px 14px;
      border-radius:999px;
      border:1px solid var(--line);
      background:#fffaf4;
    }
    nav a.active {
      background:var(--accent);
      border-color:var(--accent);
      color:white;
    }
    main {
      padding:24px;
      display:grid;
      gap:18px;
    }
    section, article, .panel {
      background:var(--panel);
      border:1px solid var(--line);
      border-radius:20px;
      padding:18px;
      box-shadow:0 12px 28px rgba(23,33,36,.06);
    }
    h2, h3 { margin:0 0 12px; }
    table {
      width:100%;
      border-collapse:collapse;
      font-size:14px;
    }
    th, td {
      text-align:left;
      padding:10px 8px;
      border-top:1px solid var(--line);
      vertical-align:top;
    }
    th { color:var(--muted); font-weight:600; }
    .grid {
      display:grid;
      gap:18px;
    }
    .agent-grid {
      grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    }
    .two-col {
      grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    }
    .card-header {
      display:flex;
      justify-content:space-between;
      align-items:flex-start;
      gap:12px;
      margin-bottom:12px;
    }
    .mono {
      font-family:"JetBrains Mono","SFMono-Regular",monospace;
      font-size:12px;
    }
    form { margin:0; }
    input[type=text], textarea, select {
      width:100%;
      padding:9px 10px;
      border:1px solid var(--line);
      border-radius:12px;
      background:#fff;
      font: inherit;
    }
    textarea { min-height:72px; resize:vertical; }
    label { display:block; font-size:14px; }
    .row {
      display:grid;
      gap:10px;
      grid-template-columns:repeat(auto-fit, minmax(180px, 1fr));
    }
    .cap-grid {
      display:grid;
      gap:10px;
      grid-template-columns:repeat(auto-fit, minmax(220px, 1fr));
      margin-top:12px;
    }
    .cap-item {
      border:1px solid var(--line);
      border-radius:16px;
      padding:12px;
      background:#fffdfa;
    }
    .tag-list {
      display:flex;
      flex-wrap:wrap;
      gap:8px;
      margin:8px 0 0;
    }
    .tag {
      display:inline-flex;
      align-items:center;
      gap:6px;
      border-radius:999px;
      padding:4px 10px;
      background:var(--accent-soft);
      color:#7f3d22;
      font-size:12px;
    }
    .btn {
      display:inline-flex;
      align-items:center;
      justify-content:center;
      border:0;
      border-radius:999px;
      padding:9px 16px;
      background:var(--accent);
      color:white;
      cursor:pointer;
      text-decoration:none;
      font: inherit;
    }
    .btn.secondary { background:#576f72; }
    .btn.danger { background:var(--danger); }
    .status-pill {
      display:inline-flex;
      align-items:center;
      justify-content:center;
      border-radius:999px;
      padding:4px 10px;
      font-size:12px;
      font-weight:600;
    }
    .status-ok { background:#dff2e8; color:var(--ok); }
    .status-running { background:#dfeaf4; color:var(--running); }
    .status-waiting { background:#f7ebd2; color:var(--wait); }
    .status-rejected { background:#f7dfe0; color:var(--danger); }
    .status-failed { background:#f3d4d6; color:var(--danger); }
    .status-neutral { background:#ece7dd; color:#555; }
    .workflow-board {
      overflow-x:auto;
      padding-bottom:8px;
    }
    .workflow-track {
      display:flex;
      align-items:center;
      gap:16px;
      min-width:max-content;
      padding:8px 4px;
    }
    .workflow-node {
      min-width:220px;
      max-width:260px;
      border:1px solid var(--line);
      border-radius:18px;
      padding:14px;
      background:white;
      position:relative;
    }
    .workflow-node .meta {
      font-size:12px;
      color:var(--muted);
      margin-bottom:8px;
      text-transform:uppercase;
      letter-spacing:.04em;
    }
    .workflow-arrow {
      color:var(--muted);
      font-size:22px;
      line-height:1;
    }
    .summary-grid {
      display:grid;
      gap:14px;
      grid-template-columns:repeat(auto-fit, minmax(220px, 1fr));
    }
    .summary-item {
      border:1px solid var(--line);
      border-radius:16px;
      padding:14px;
      background:#fffdfa;
    }
    .small { font-size:13px; }
    .spacer { height:6px; }
  </style>
</head>
<body>
  <header>
    <h1>WeClaw 控制台</h1>
    <p>多账号用户 Agent、自动分发、审批流与工作流画板。</p>
    <nav>
      <a href="/console/agents" class="{{if eq .Active "agents"}}active{{end}}">Agent</a>
      <a href="/console/tasks" class="{{if eq .Active "tasks"}}active{{end}}">任务</a>
      <a href="/console/approvals" class="{{if eq .Active "approvals"}}active{{end}}">审批</a>
      <a href="/console/audit" class="{{if eq .Active "audit"}}active{{end}}">审计</a>
    </nav>
  </header>
  <main>
{{end}}

{{define "layout_end"}}
  </main>
</body>
</html>
{{end}}

{{define "agents_page"}}
{{template "layout_start" .}}
<section>
  <div class="card-header">
    <div>
      <h2>用户 Agent</h2>
      <p class="muted">每个 Agent 使用卡片式管理，可维护主身份、专长画像、能力开关与 A2A Card。</p>
    </div>
  </div>
  <div class="grid agent-grid">
    {{range .Profiles}}
    <article>
      <div class="card-header">
        <div>
          <h3>{{.Profile.DisplayName}}</h3>
          <div class="mono muted">{{.Profile.AccountID}}</div>
        </div>
        <span class="status-pill {{statusClass .DelegationStatus}}">{{.DelegationStatusLabel}}</span>
      </div>

        <form method="post" action="/console/users/{{.EscapedAccountID}}/update">
          <div class="row">
            <label>显示名
              <input type="text" name="display_name" value="{{.Profile.DisplayName}}">
            </label>
          <label>主人联系人 ID
            <input type="text" name="owner_contact_id" value="{{.Profile.OwnerContactID}}">
            </label>
            <label>底层 Agent
              {{$currentBase := .ProfileBase}}
              <select name="base_agent_name">
                {{range $.AvailableBaseAgents}}
                <option value="{{.}}" {{selected $currentBase .}}>{{.}}</option>
                {{end}}
              </select>
            </label>
        </div>
        <div class="spacer"></div>
        <label>简介
          <textarea name="description">{{.Profile.Description}}</textarea>
        </label>
        <div class="spacer"></div>
        <div class="row">
          <label>专长标签（每行一个）
            <textarea name="specialization_tags">{{joinLines .Profile.SpecializationTags}}</textarea>
          </label>
          <label>典型任务（每行一个）
            <textarea name="specialization_examples">{{joinLines .Profile.SpecializationExamples}}</textarea>
          </label>
          <label>禁做事项（每行一个）
            <textarea name="specialization_avoid">{{joinLines .Profile.SpecializationAvoid}}</textarea>
          </label>
        </div>
        <div class="spacer"></div>
        <label class="small">
          <input type="checkbox" name="delegation_enabled" value="1" {{checked .Profile.DelegationEnabled}}>
          允许被自动分发命中
        </label>
        <div class="spacer"></div>
        <button class="btn" type="submit">保存 Agent 卡片</button>
      </form>

      <div class="tag-list">
        {{range .Profile.SpecializationTags}}<span class="tag">{{.}}</span>{{end}}
      </div>

      <form method="post" action="/console/users/{{.EscapedAccountID}}/capabilities" style="margin-top:14px;">
        <div class="cap-grid">
          {{range .Bindings}}
          <label class="cap-item">
            <input type="checkbox" name="capability" value="{{.CapabilityID}}" {{checked .Enabled}}>
            <strong>{{.Name}}</strong>
            <div class="muted small">{{.Description}}</div>
            <div class="tag-list">
              {{range .RoutingTags}}<span class="tag">{{.}}</span>{{end}}
            </div>
          </label>
          {{end}}
        </div>
        <div class="spacer"></div>
        <button class="btn secondary" type="submit">保存能力开关</button>
      </form>

      <div class="spacer"></div>
      <div class="mono">A2A URL: <a href="{{.CardURL}}">{{.CardURL}}</a></div>
      <details style="margin-top:10px;">
        <summary>查看 A2A Card</summary>
        <pre>{{.CardJSON}}</pre>
      </details>
    </article>
    {{end}}
  </div>
</section>
{{template "layout_end" .}}
{{end}}

{{define "tasks_page"}}
{{template "layout_start" .}}
<section>
  <div class="card-header">
    <div>
      <h2>任务列表</h2>
      <p class="muted">查看任务状态、结果与详情入口。详情页内包含工作流画板。</p>
    </div>
  </div>
  <table>
    <thead>
      <tr><th>任务</th><th>状态</th><th>请求方</th><th>目标方</th><th>标题</th><th>输出</th><th>视图</th></tr>
    </thead>
    <tbody>
      {{range .Tasks}}
      <tr>
        <td class="mono">{{shortID .ID}}</td>
        <td><span class="status-pill {{statusClass .Status}}">{{.Status}}</span></td>
        <td>{{label .RequesterAccountID $.AccountLabels}}</td>
        <td>{{label .TargetAccountID $.AccountLabels}}</td>
        <td>{{.Title}}</td>
        <td>{{taskOutput .}}</td>
        <td>
          <div style="display:flex; gap:8px; flex-wrap:wrap;">
            <a class="btn secondary" href="/console/tasks/{{.EscapedID}}?view=canvas">画布</a>
            <a class="btn secondary" href="/console/tasks/{{.EscapedID}}?view=list">列表</a>
          </div>
        </td>
      </tr>
      {{else}}
      <tr><td colspan="7" class="muted">暂无任务。</td></tr>
      {{end}}
    </tbody>
  </table>
</section>
{{template "layout_end" .}}
{{end}}

{{define "approvals_page"}}
{{template "layout_start" .}}
<section>
  <div class="card-header">
    <div>
      <h2>审批列表</h2>
      <p class="muted">管理待审批和已处理的协作授权。</p>
    </div>
  </div>
  <table>
    <thead>
      <tr><th>审批码</th><th>任务</th><th>请求方</th><th>审批方</th><th>状态</th><th>请求内容</th><th>任务视图</th><th>操作</th></tr>
    </thead>
    <tbody>
      {{range .Approvals}}
      <tr>
        <td class="mono">{{shortID .ID}}</td>
        <td><a href="/console/tasks/{{.EscapedTaskID}}">{{shortID .TaskID}}</a></td>
        <td>{{label .RequesterAccountID $.AccountLabels}}</td>
        <td>{{label .ApproverAccountID $.AccountLabels}}</td>
        <td><span class="status-pill {{statusClass .Status}}">{{.Status}}</span></td>
        <td>{{.RequestReason}}</td>
        <td>
          <div style="display:flex; gap:8px; flex-wrap:wrap;">
            <a class="btn secondary" href="/console/tasks/{{.EscapedTaskID}}?view=canvas">画布</a>
            <a class="btn secondary" href="/console/tasks/{{.EscapedTaskID}}?view=list">列表</a>
          </div>
        </td>
        <td>
          {{if eq .Status "pending"}}
          <form method="post" action="/console/approvals/{{.ID}}/approve" style="display:inline-block;">
            <button class="btn" type="submit">批准</button>
          </form>
          <form method="post" action="/console/approvals/{{.ID}}/reject" style="display:inline-block;">
            <input type="text" name="reason" placeholder="拒绝原因">
            <button class="btn danger" type="submit">拒绝</button>
          </form>
          {{else}}
          <span class="muted">{{.ResolvedReason}}</span>
          {{end}}
        </td>
      </tr>
      {{else}}
      <tr><td colspan="8" class="muted">暂无审批记录。</td></tr>
      {{end}}
    </tbody>
  </table>
</section>
{{template "layout_end" .}}
{{end}}

{{define "audit_page"}}
{{template "layout_start" .}}
<section>
  <div class="card-header">
    <div>
      <h2>审计日志</h2>
      <p class="muted">集中查看自动分发、审批、执行与回传事件。</p>
    </div>
  </div>
  <table>
    <thead>
      <tr><th>时间</th><th>账号</th><th>分类</th><th>信息</th><th>任务</th><th>视图</th></tr>
    </thead>
    <tbody>
      {{range .Audit}}
      <tr>
        <td class="mono">{{.CreatedAt}}</td>
        <td>{{label .AccountID $.AccountLabels}}</td>
        <td>{{.Category}}</td>
        <td>{{.Message}}</td>
        <td>{{if .TaskID}}<a href="/console/tasks/{{.EscapedTaskID}}">{{shortID .TaskID}}</a>{{end}}</td>
        <td>
          {{if .TaskID}}
          <div style="display:flex; gap:8px; flex-wrap:wrap;">
            <a class="btn secondary" href="/console/tasks/{{.EscapedTaskID}}?view=canvas">画布</a>
            <a class="btn secondary" href="/console/tasks/{{.EscapedTaskID}}?view=list">列表</a>
          </div>
          {{end}}
        </td>
      </tr>
      {{else}}
      <tr><td colspan="6" class="muted">暂无审计日志。</td></tr>
      {{end}}
    </tbody>
  </table>
</section>
{{template "layout_end" .}}
{{end}}

{{define "task_detail_page"}}
{{template "layout_start" .}}
<section>
  <div class="card-header">
    <div>
      <h2>任务详情</h2>
      <p class="muted">查看状态摘要、工作流画板、历史记录与审计事件。</p>
    </div>
    <a class="btn secondary" href="/console/tasks">返回任务列表</a>
  </div>

  <div class="summary-grid">
    <div class="summary-item">
      <div class="muted small">任务 ID</div>
      <div class="mono">{{.TaskDetail.Task.ID}}</div>
    </div>
    <div class="summary-item">
      <div class="muted small">状态</div>
      <div><span class="status-pill {{statusClass .TaskDetail.Task.Status}}">{{.TaskDetail.Task.Status}}</span></div>
    </div>
    <div class="summary-item">
      <div class="muted small">请求方</div>
      <div>{{label .TaskDetail.Task.RequesterAccountID .AccountLabels}}</div>
    </div>
    <div class="summary-item">
      <div class="muted small">目标方</div>
      <div>{{label .TaskDetail.Task.TargetAccountID .AccountLabels}}</div>
    </div>
  </div>

  <div class="spacer"></div>
  <div class="summary-item">
    <div class="muted small">标题</div>
    <div>{{.TaskDetail.Task.Title}}</div>
  </div>
  <div class="spacer"></div>
  <div class="summary-item">
    <div class="muted small">结果 / 错误</div>
    <div>{{taskOutput .TaskDetail.Task}}</div>
  </div>
  {{if .TaskDetail.Approval}}
  <div class="spacer"></div>
  <div class="summary-item">
    <div class="muted small">授权</div>
    <div>{{.TaskDetail.Approval.ID}} <span class="status-pill {{statusClass .TaskDetail.Approval.Status}}">{{.TaskDetail.Approval.Status}}</span></div>
  </div>
  {{end}}
</section>

<section>
  <div class="card-header">
    <div>
      <h2>工作流画板</h2>
      <p class="muted">像 Coze / Dify 一样观察当前任务走到了哪一步、卡在哪个节点。</p>
    </div>
    <div style="display:flex; gap:10px; flex-wrap:wrap;">
      <a class="btn {{if eq .WorkflowView "canvas"}}secondary{{end}}" href="/console/tasks/{{.TaskDetail.Task.ID}}?view=canvas">画布视图</a>
      <a class="btn {{if eq .WorkflowView "list"}}secondary{{end}}" href="/console/tasks/{{.TaskDetail.Task.ID}}?view=list">列表视图</a>
      <a class="btn secondary" href="/console/tasks/{{.TaskDetail.Task.ID}}/workflow.json">workflow.json</a>
    </div>
  </div>
  {{if .TaskDetail.Workflow.BlockedReason}}
  <p class="muted">当前卡点：{{.TaskDetail.Workflow.BlockedReason}}</p>
  {{end}}
  {{if eq .WorkflowView "list"}}
  <table>
    <thead><tr><th>步骤</th><th>类型</th><th>状态</th><th>说明</th></tr></thead>
    <tbody>
      {{range .TaskDetail.Workflow.Nodes}}
      <tr>
        <td>{{.Label}}</td>
        <td>{{.Type}}</td>
        <td><span class="status-pill {{statusClass .Status}}">{{.Status}}</span></td>
        <td>{{.Detail}}</td>
      </tr>
      {{else}}
      <tr><td colspan="4" class="muted">暂无工作流节点。</td></tr>
      {{end}}
    </tbody>
  </table>
  {{else}}
  <div class="workflow-board">
    <div class="workflow-track">
      {{range .TaskDetail.Workflow.Nodes}}
      <div class="workflow-node">
        <div class="meta">{{.Type}}</div>
        <h3>{{.Label}}</h3>
        <div><span class="status-pill {{statusClass .Status}}">{{.Status}}</span></div>
        {{if .Detail}}<p class="small">{{.Detail}}</p>{{end}}
      </div>
      {{if lt .Order $.LastWorkflowIndex}}<div class="workflow-arrow">→</div>{{end}}
      {{end}}
    </div>
  </div>
  {{end}}
</section>

<section class="grid two-col">
  <article>
    <h2>任务历史</h2>
    <table>
      <thead><tr><th>时间</th><th>动作</th><th>内容</th></tr></thead>
      <tbody>
        {{range .TaskDetail.Task.History}}
        <tr>
          <td class="mono">{{.CreatedAt}}</td>
          <td>{{.Kind}}</td>
          <td>{{.Message}}</td>
        </tr>
        {{else}}
        <tr><td colspan="3" class="muted">暂无历史记录。</td></tr>
        {{end}}
      </tbody>
    </table>
  </article>
  <article>
    <h2>任务审计</h2>
    <table>
      <thead><tr><th>时间</th><th>分类</th><th>信息</th></tr></thead>
      <tbody>
        {{range .TaskDetail.Audit}}
        <tr>
          <td class="mono">{{.CreatedAt}}</td>
          <td>{{.Category}}</td>
          <td>{{.Message}}</td>
        </tr>
        {{else}}
        <tr><td colspan="3" class="muted">暂无审计记录。</td></tr>
        {{end}}
      </tbody>
    </table>
  </article>
</section>
{{template "layout_end" .}}
{{end}}
`))

type consoleAgentView struct {
	Profile               controlplane.UserAgentProfile
	Bindings              []controlplane.CapabilityBinding
	EscapedAccountID      string
	CardURL               string
	CardJSON              string
	ProfileBase           string
	DelegationStatus      string
	DelegationStatusLabel string
}

type consoleTaskView struct {
	controlplane.TaskRecord
	EscapedID string
}

type consoleApprovalView struct {
	controlplane.AuthorizationGrant
	EscapedTaskID string
}

type consoleAuditView struct {
	controlplane.AuditRecord
	EscapedTaskID string
}

type consolePageData struct {
	Title               string
	Active              string
	Profiles            []consoleAgentView
	Tasks               []consoleTaskView
	Approvals           []consoleApprovalView
	Audit               []consoleAuditView
	TaskDetail          *controlplane.TaskDetail
	AccountLabels       map[string]string
	AvailableBaseAgents []string
	LastWorkflowIndex   int
	WorkflowView        string
}

func (s *Server) handleWellKnownAgentCard(w http.ResponseWriter, r *http.Request) {
	if s.userAgents == nil {
		http.Error(w, "user agent service unavailable", http.StatusServiceUnavailable)
		return
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	if accountID == "" {
		profiles, err := s.userAgents.ListProfiles()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(profiles) == 1 {
			accountID = profiles[0].AccountID
		} else {
			http.Error(w, "multiple user agents configured; use /a2a/users/{account}/agent-card.json or ?account_id=", http.StatusBadRequest)
			return
		}
	}
	s.renderAgentCard(w, accountID)
}

func (s *Server) handleA2AUser(w http.ResponseWriter, r *http.Request) {
	if s.userAgents == nil {
		http.Error(w, "user agent service unavailable", http.StatusServiceUnavailable)
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/a2a/users/")
	trimmed = strings.Trim(trimmed, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	accountID, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid account id", http.StatusBadRequest)
		return
	}

	if len(parts) == 2 && parts[1] == "agent-card.json" {
		s.renderAgentCard(w, accountID)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodGet {
		s.renderAgentCard(w, accountID)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var request controlplane.JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.writeJSONRPCError(w, request.ID, -32700, "invalid JSON")
		return
	}

	result, err := s.userAgents.HandleJSONRPC(r.Context(), accountID, request)
	if err != nil {
		code := -32000
		if strings.Contains(err.Error(), "unsupported method") {
			code = -32601
		}
		s.writeJSONRPCError(w, request.ID, code, err.Error())
		return
	}

	s.writeJSON(w, controlplane.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      request.ID,
		Result:  result,
	})
}

func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	http.Redirect(w, r, "/console/agents", http.StatusSeeOther)
}

func (s *Server) handleConsoleRoute(w http.ResponseWriter, r *http.Request) {
	if s.userAgents == nil {
		http.Error(w, "user agent service unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodPost {
		s.handleConsoleAction(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	trimmed := strings.Trim(strings.TrimPrefix(r.URL.Path, "/console/"), "/")
	if trimmed == "" {
		http.Redirect(w, r, "/console/agents", http.StatusSeeOther)
		return
	}
	parts := strings.Split(trimmed, "/")

	switch {
	case len(parts) == 1 && parts[0] == "agents":
		s.renderAgentsPage(w, r)
	case len(parts) == 1 && parts[0] == "tasks":
		s.renderTasksPage(w, r)
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "workflow.json":
		s.renderTaskWorkflow(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "tasks":
		s.renderTaskDetailPage(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "approvals":
		s.renderApprovalsPage(w, r)
	case len(parts) == 1 && parts[0] == "audit":
		s.renderAuditPage(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) renderAgentsPage(w http.ResponseWriter, _ *http.Request) {
	profiles, err := s.userAgents.ListProfiles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	availableBaseAgents := s.userAgents.AvailableBaseAgents()

	views := make([]consoleAgentView, 0, len(profiles))
	for _, profile := range profiles {
		card, err := s.userAgents.BuildAgentCard(profile.AccountID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rawCard, err := json.MarshalIndent(card, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bindings, err := s.userAgents.ListCapabilityBindings(profile.AccountID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status := "running"
		label := "可自动委派"
		if !profile.DelegationEnabled {
			status = "rejected"
			label = "暂停自动委派"
		}
		views = append(views, consoleAgentView{
			Profile:               profile,
			Bindings:              bindings,
			EscapedAccountID:      url.PathEscape(profile.AccountID),
			CardURL:               s.agentCardURL(profile.AccountID),
			CardJSON:              string(rawCard),
			ProfileBase:           profile.BaseAgentName,
			DelegationStatus:      status,
			DelegationStatusLabel: label,
		})
	}

	s.renderConsoleTemplate(w, "agents_page", consolePageData{
		Title:               "WeClaw 控制台 / Agent",
		Active:              "agents",
		Profiles:            views,
		AvailableBaseAgents: availableBaseAgents,
	})
}

func (s *Server) renderTasksPage(w http.ResponseWriter, _ *http.Request) {
	tasks, err := s.userAgents.ListTasks(100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	labels, err := s.accountLabels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]consoleTaskView, 0, len(tasks))
	for _, task := range tasks {
		views = append(views, consoleTaskView{
			TaskRecord: task,
			EscapedID:  url.PathEscape(task.ID),
		})
	}
	s.renderConsoleTemplate(w, "tasks_page", consolePageData{
		Title:         "WeClaw 控制台 / 任务",
		Active:        "tasks",
		Tasks:         views,
		AccountLabels: labels,
	})
}

func (s *Server) renderTaskDetailPage(w http.ResponseWriter, r *http.Request, rawTaskID string) {
	taskID, err := url.PathUnescape(rawTaskID)
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	detail, err := s.userAgents.GetTaskDetail(taskID, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if detail == nil {
		http.NotFound(w, nil)
		return
	}
	labels, err := s.accountLabels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	lastIdx := -1
	if detail.Workflow != nil {
		lastIdx = len(detail.Workflow.Nodes) - 1
	}
	view := strings.TrimSpace(r.URL.Query().Get("view"))
	if view != "list" {
		view = "canvas"
	}
	s.renderConsoleTemplate(w, "task_detail_page", consolePageData{
		Title:             "WeClaw 控制台 / 任务详情",
		Active:            "tasks",
		TaskDetail:        detail,
		AccountLabels:     labels,
		LastWorkflowIndex: lastIdx,
		WorkflowView:      view,
	})
}

func (s *Server) renderTaskWorkflow(w http.ResponseWriter, _ *http.Request, rawTaskID string) {
	taskID, err := url.PathUnescape(rawTaskID)
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	detail, err := s.userAgents.GetTaskDetail(taskID, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if detail == nil || detail.Workflow == nil {
		http.NotFound(w, nil)
		return
	}
	s.writeJSON(w, detail.Workflow)
}

func (s *Server) renderApprovalsPage(w http.ResponseWriter, _ *http.Request) {
	approvals, err := s.userAgents.ListApprovals("", 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	labels, err := s.accountLabels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]consoleApprovalView, 0, len(approvals))
	for _, approval := range approvals {
		views = append(views, consoleApprovalView{
			AuthorizationGrant: approval,
			EscapedTaskID:      url.PathEscape(approval.TaskID),
		})
	}
	s.renderConsoleTemplate(w, "approvals_page", consolePageData{
		Title:         "WeClaw 控制台 / 审批",
		Active:        "approvals",
		Approvals:     views,
		AccountLabels: labels,
	})
}

func (s *Server) renderAuditPage(w http.ResponseWriter, _ *http.Request) {
	labels, err := s.accountLabels()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	records, err := s.userAgents.ListAudit(200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]consoleAuditView, 0, len(records))
	for _, record := range records {
		views = append(views, consoleAuditView{
			AuditRecord:   record,
			EscapedTaskID: url.PathEscape(record.TaskID),
		})
	}
	s.renderConsoleTemplate(w, "audit_page", consolePageData{
		Title:         "WeClaw 控制台 / 审计",
		Active:        "audit",
		Audit:         views,
		AccountLabels: labels,
	})
}

func (s *Server) handleConsoleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	trimmed := strings.Trim(strings.TrimPrefix(r.URL.Path, "/console/"), "/")
	parts := strings.Split(trimmed, "/")

	switch {
	case len(parts) == 3 && parts[0] == "users" && parts[2] == "update":
		accountID, err := url.PathUnescape(parts[1])
		if err != nil {
			http.Error(w, "invalid account", http.StatusBadRequest)
			return
		}
		if err := s.userAgents.UpdateProfile(controlplane.UpdateProfileInput{
			AccountID:              accountID,
			DisplayName:            r.FormValue("display_name"),
			Description:            r.FormValue("description"),
			OwnerContactID:         r.FormValue("owner_contact_id"),
			BaseAgentName:          r.FormValue("base_agent_name"),
			SpecializationTags:     parseLines(r.FormValue("specialization_tags")),
			SpecializationExamples: parseLines(r.FormValue("specialization_examples")),
			SpecializationAvoid:    parseLines(r.FormValue("specialization_avoid")),
			DelegationEnabled:      r.FormValue("delegation_enabled") == "1",
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		redirectBack(w, r, "/console/agents")
		return

	case len(parts) == 3 && parts[0] == "users" && parts[2] == "capabilities":
		accountID, err := url.PathUnescape(parts[1])
		if err != nil {
			http.Error(w, "invalid account", http.StatusBadRequest)
			return
		}
		if err := s.userAgents.ReplaceCapabilityBindings(accountID, r.Form["capability"]); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		redirectBack(w, r, "/console/agents")
		return

	case len(parts) == 3 && parts[0] == "approvals" && parts[2] == "approve":
		if _, err := s.userAgents.ApproveGrant(r.Context(), parts[1], "console"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		redirectBack(w, r, "/console/approvals")
		return

	case len(parts) == 3 && parts[0] == "approvals" && parts[2] == "reject":
		if _, err := s.userAgents.RejectGrant(r.Context(), parts[1], r.FormValue("reason")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		redirectBack(w, r, "/console/approvals")
		return
	}

	http.NotFound(w, r)
}

func (s *Server) renderAgentCard(w http.ResponseWriter, accountID string) {
	card, err := s.userAgents.BuildAgentCard(accountID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if card == nil {
		http.Error(w, "agent card not found", http.StatusNotFound)
		return
	}
	s.writeJSON(w, card)
}

func (s *Server) renderConsoleTemplate(w http.ResponseWriter, name string, data consolePageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := consoleTemplates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) accountLabels() (map[string]string, error) {
	profiles, err := s.userAgents.ListProfiles()
	if err != nil {
		return nil, err
	}
	labels := make(map[string]string, len(profiles))
	for _, profile := range profiles {
		labels[profile.AccountID] = firstNonBlank(profile.DisplayName, profile.AccountID)
	}
	return labels, nil
}

func (s *Server) writeJSONRPCError(w http.ResponseWriter, id any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(controlplane.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &controlplane.JSONRPCError{
			Code:    code,
			Message: message,
		},
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload)
}

func (s *Server) agentCardURL(accountID string) string {
	base := "http://" + s.addr
	if strings.HasPrefix(base, "http://0.0.0.0:") {
		base = strings.Replace(base, "http://0.0.0.0:", "http://127.0.0.1:", 1)
	}
	return base + "/a2a/users/" + url.PathEscape(accountID) + "/agent-card.json"
}

func parseLines(raw string) []string {
	lines := strings.Split(raw, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func redirectBack(w http.ResponseWriter, r *http.Request, fallback string) {
	if ref := strings.TrimSpace(r.Referer()); ref != "" {
		http.Redirect(w, r, ref, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fallback, http.StatusSeeOther)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
