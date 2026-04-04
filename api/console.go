package api

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/fastclaw-ai/weclaw/controlplane"
)

var consoleTemplate = template.Must(template.New("console").Funcs(template.FuncMap{
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
}).Parse(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <title>WeClaw 管理页</title>
  <style>
    :root { color-scheme: light; --bg:#f5f1e8; --card:#fffdf9; --ink:#182022; --line:#d7cbb7; --accent:#b85c38; --muted:#6a6f73; }
    body { margin:0; font-family:"Noto Sans SC","PingFang SC",sans-serif; background:linear-gradient(180deg,#f8f3ea,#efe6d5); color:var(--ink); }
    header { padding:24px 28px 12px; }
    h1 { margin:0; font-size:28px; }
    p { color:var(--muted); }
    main { display:grid; gap:18px; padding:0 24px 24px; }
    section { background:var(--card); border:1px solid var(--line); border-radius:18px; padding:18px; box-shadow:0 10px 25px rgba(24,32,34,.06); }
    h2 { margin:0 0 14px; font-size:20px; }
    table { width:100%; border-collapse:collapse; font-size:14px; }
    th, td { padding:10px 8px; border-top:1px solid var(--line); vertical-align:top; text-align:left; }
    th { color:var(--muted); font-weight:600; }
    form { margin:0; }
    input[type=text], textarea, select { width:100%; box-sizing:border-box; padding:8px 10px; border:1px solid var(--line); border-radius:10px; background:#fff; }
    textarea { min-height:72px; resize:vertical; }
    .row { display:grid; gap:10px; grid-template-columns:repeat(auto-fit,minmax(180px,1fr)); }
    .cap-grid { display:grid; gap:8px; grid-template-columns:repeat(auto-fit,minmax(220px,1fr)); margin-top:10px; }
    .cap-item { border:1px solid var(--line); border-radius:12px; padding:10px; }
    .btn { display:inline-block; border:0; border-radius:999px; padding:8px 14px; background:var(--accent); color:#fff; cursor:pointer; }
    .btn.secondary { background:#576f72; }
    .btn.danger { background:#a63d40; }
    .mono { font-family:"JetBrains Mono","SFMono-Regular",monospace; font-size:12px; }
    .muted { color:var(--muted); }
    details { margin-top:10px; }
    pre { overflow:auto; background:#171a1c; color:#f6f7f9; border-radius:12px; padding:12px; }
  </style>
</head>
<body>
  <header>
    <h1>WeClaw 管理页</h1>
    <p>管理多账号用户 Agent、A2A Card、协作任务、授权与审计日志。</p>
  </header>
  <main>
    <section>
      <h2>用户 Agent</h2>
      {{range .Profiles}}
      <article style="border-top:1px solid var(--line); padding-top:16px; margin-top:16px;">
        <h3 style="margin:0 0 10px;">{{.Profile.DisplayName}} <span class="mono muted">{{.Profile.AccountID}}</span></h3>
        <form method="post" action="/console/users/{{.EscapedAccountID}}/update">
          <div class="row">
            <label>显示名<input type="text" name="display_name" value="{{.Profile.DisplayName}}"></label>
            <label>主人联系人 ID<input type="text" name="owner_contact_id" value="{{.Profile.OwnerContactID}}"></label>
            <label>底层 Agent
              {{$current := .CurrentProfile.BaseAgentName}}
              <select name="base_agent_name">
                {{range $.AvailableBaseAgents}}
                <option value="{{.}}" {{selected $current .}}>{{.}}</option>
                {{end}}
              </select>
            </label>
          </div>
          <label style="display:block; margin-top:10px;">描述<textarea name="description">{{.Profile.Description}}</textarea></label>
          <div style="margin-top:10px;"><button class="btn" type="submit">保存用户 Agent</button></div>
        </form>

        <form method="post" action="/console/users/{{.EscapedAccountID}}/capabilities">
          <div class="cap-grid">
            {{range .Bindings}}
            <label class="cap-item">
              <input type="checkbox" name="capability" value="{{.CapabilityID}}" {{checked .Enabled}}>
              <strong>{{.Name}}</strong>
              <div class="muted">{{.Description}}</div>
              <div class="mono muted">{{.RiskLevel}} | {{.ImplementationHint}}</div>
            </label>
            {{end}}
          </div>
          <div style="margin-top:10px;"><button class="btn secondary" type="submit">保存能力开关</button></div>
        </form>

        <p class="mono">A2A URL: <a href="{{.CardURL}}">{{.CardURL}}</a></p>
        <details>
          <summary>查看 A2A Card</summary>
          <pre>{{.CardJSON}}</pre>
        </details>
      </article>
      {{end}}
    </section>

    <section>
      <h2>协作授权</h2>
      <table>
        <thead><tr><th>授权编号</th><th>任务</th><th>请求方</th><th>审批方</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>
          {{range .Approvals}}
          <tr>
            <td class="mono">{{shortID .ID}}</td>
            <td>{{.TaskID}}</td>
            <td>{{label .RequesterAccountID $.AccountLabels}}</td>
            <td>{{label .ApproverAccountID $.AccountLabels}}</td>
            <td>{{.Status}}</td>
            <td>
              {{if eq .Status "pending"}}
              <form method="post" action="/console/approvals/{{.ID}}/approve" style="display:inline-block;"><button class="btn" type="submit">批准</button></form>
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
          <tr><td colspan="6" class="muted">暂无授权记录。</td></tr>
          {{end}}
        </tbody>
      </table>
    </section>

    <section>
      <h2>任务</h2>
      <table>
        <thead><tr><th>任务</th><th>状态</th><th>请求方</th><th>执行方</th><th>标题</th><th>结果</th></tr></thead>
        <tbody>
          {{range .Tasks}}
          <tr>
            <td class="mono">{{shortID .ID}}</td>
            <td>{{.Status}}</td>
            <td>{{label .RequesterAccountID $.AccountLabels}}</td>
            <td>{{label .TargetAccountID $.AccountLabels}}</td>
            <td>{{.Title}}</td>
            <td>{{if .ResultText}}{{.ResultText}}{{else}}{{.ErrorText}}{{end}}</td>
          </tr>
          {{else}}
          <tr><td colspan="6" class="muted">暂无任务。</td></tr>
          {{end}}
        </tbody>
      </table>
    </section>

    <section>
      <h2>审计日志</h2>
      <table>
        <thead><tr><th>时间</th><th>账号</th><th>分类</th><th>信息</th></tr></thead>
        <tbody>
          {{range .Audit}}
          <tr>
            <td class="mono">{{.CreatedAt}}</td>
            <td>{{label .AccountID $.AccountLabels}}</td>
            <td>{{.Category}}</td>
            <td>{{.Message}}</td>
          </tr>
          {{else}}
          <tr><td colspan="4" class="muted">暂无审计日志。</td></tr>
          {{end}}
        </tbody>
      </table>
    </section>
  </main>
</body>
</html>`))

type consoleProfileView struct {
	Profile          controlplane.UserAgentProfile
	CurrentProfile   controlplane.UserAgentProfile
	Bindings         []controlplane.CapabilityBinding
	EscapedAccountID string
	CardURL          string
	CardJSON         string
}

type consolePageData struct {
	Profiles            []consoleProfileView
	Tasks               []controlplane.TaskRecord
	Approvals           []controlplane.AuthorizationGrant
	Audit               []controlplane.AuditRecord
	AccountLabels       map[string]string
	AvailableBaseAgents []string
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
	if s.userAgents == nil {
		http.Error(w, "user agent service unavailable", http.StatusServiceUnavailable)
		return
	}

	snapshot, err := s.userAgents.Snapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	availableBaseAgents := s.userAgents.AvailableBaseAgents()

	accountLabels := make(map[string]string, len(snapshot.Profiles))
	profiles := make([]consoleProfileView, 0, len(snapshot.Profiles))
	for _, profile := range snapshot.Profiles {
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
		accountLabels[profile.AccountID] = firstNonBlank(profile.DisplayName, profile.AccountID)
		profiles = append(profiles, consoleProfileView{
			Profile:          profile,
			CurrentProfile:   profile,
			Bindings:         snapshot.Bindings[profile.AccountID],
			EscapedAccountID: url.PathEscape(profile.AccountID),
			CardURL:          s.agentCardURL(profile.AccountID),
			CardJSON:         string(rawCard),
		})
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := consoleTemplate.Execute(w, consolePageData{
		Profiles:            profiles,
		Tasks:               snapshot.Tasks,
		Approvals:           snapshot.Approvals,
		Audit:               snapshot.Audit,
		AccountLabels:       accountLabels,
		AvailableBaseAgents: availableBaseAgents,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleConsoleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.userAgents == nil {
		http.Error(w, "user agent service unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/console/")
	trimmed = strings.Trim(trimmed, "/")
	parts := strings.Split(trimmed, "/")

	switch {
	case len(parts) == 3 && parts[0] == "users" && parts[2] == "update":
		accountID, err := url.PathUnescape(parts[1])
		if err != nil {
			http.Error(w, "invalid account", http.StatusBadRequest)
			return
		}
		if err := s.userAgents.UpdateProfile(controlplane.UpdateProfileInput{
			AccountID:      accountID,
			DisplayName:    r.FormValue("display_name"),
			Description:    r.FormValue("description"),
			OwnerContactID: r.FormValue("owner_contact_id"),
			BaseAgentName:  r.FormValue("base_agent_name"),
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/console", http.StatusSeeOther)
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
		http.Redirect(w, r, "/console", http.StatusSeeOther)
		return

	case len(parts) == 3 && parts[0] == "approvals" && parts[2] == "approve":
		if _, err := s.userAgents.ApproveGrant(r.Context(), parts[1], "console"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/console", http.StatusSeeOther)
		return

	case len(parts) == 3 && parts[0] == "approvals" && parts[2] == "reject":
		if _, err := s.userAgents.RejectGrant(r.Context(), parts[1], r.FormValue("reason")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/console", http.StatusSeeOther)
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

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
