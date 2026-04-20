package codexsession

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSessionsByCwdFiltersAndSorts(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("WECLAW_CODEX_SESSIONS_DIR", tempDir)

	projectDir := filepath.Join(tempDir, "workspace", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	writeSessionFile(t, tempDir, "2026/04/20/older.jsonl", `{"timestamp":"2026-04-20T10:00:00Z","type":"session_meta","payload":{"id":"thread-old","timestamp":"2026-04-20T10:00:00Z","cwd":"`+projectDir+`","source":"cli","originator":"codex_cli_rs"}}
{"timestamp":"2026-04-20T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>"}]}}
{"timestamp":"2026-04-20T10:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"旧会话预览"}]}}
`)
	writeSessionFile(t, tempDir, "2026/04/20/newer.jsonl", `{"timestamp":"2026-04-20T12:00:00Z","type":"session_meta","payload":{"id":"thread-new","timestamp":"2026-04-20T12:00:00Z","cwd":"`+projectDir+`","source":"exec","originator":"codex_exec"}}
{"timestamp":"2026-04-20T12:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"最新会话预览"}]}}
`)
	writeSessionFile(t, tempDir, "2026/04/20/other.jsonl", `{"timestamp":"2026-04-20T11:00:00Z","type":"session_meta","payload":{"id":"thread-other","timestamp":"2026-04-20T11:00:00Z","cwd":"`+filepath.Join(tempDir, "workspace", "other")+`","source":"cli","originator":"codex_cli_rs"}}`)

	sessions, err := FindSessionsByCwd(projectDir)
	if err != nil {
		t.Fatalf("FindSessionsByCwd returned error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].ThreadID != "thread-new" {
		t.Fatalf("expected newest session first, got %q", sessions[0].ThreadID)
	}
	if sessions[0].Preview != "最新会话预览" {
		t.Fatalf("expected preview to come from latest user message, got %q", sessions[0].Preview)
	}
	if sessions[1].Preview != "旧会话预览" {
		t.Fatalf("expected preview to skip internal messages, got %q", sessions[1].Preview)
	}
}

func TestBindingSaveLoadAndClear(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("WECLAW_CODEX_BINDINGS_PATH", filepath.Join(tempDir, "codex-bindings.json"))

	projectDir := filepath.Join(tempDir, "workspace", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	if err := SaveBinding(Binding{
		ProjectCwd: projectDir,
		ThreadID:   "thread-123",
		Preview:    "hello",
	}); err != nil {
		t.Fatalf("SaveBinding returned error: %v", err)
	}

	binding, err := LoadBinding(projectDir)
	if err != nil {
		t.Fatalf("LoadBinding returned error: %v", err)
	}
	if binding == nil {
		t.Fatalf("expected saved binding to be returned")
	}
	if binding.ThreadID != "thread-123" {
		t.Fatalf("unexpected thread id %q", binding.ThreadID)
	}
	if binding.ProjectCwd != projectDir {
		t.Fatalf("unexpected project cwd %q", binding.ProjectCwd)
	}

	if err := ClearBinding(projectDir); err != nil {
		t.Fatalf("ClearBinding returned error: %v", err)
	}
	binding, err = LoadBinding(projectDir)
	if err != nil {
		t.Fatalf("LoadBinding after clear returned error: %v", err)
	}
	if binding != nil {
		t.Fatalf("expected binding to be cleared")
	}
}

func writeSessionFile(t *testing.T, root, relativePath, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", fullPath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", fullPath, err)
	}
}
