package messaging

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtractLocalAttachmentPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	pdfPath := filepath.Join(dir, "report.pdf")
	txtPath := filepath.Join(dir, "notes.txt")
	unknownPath := filepath.Join(dir, "artifact.weclaw")
	homePath := filepath.Join(dir, "Downloads", "photo.png")
	spacedPath := filepath.Join(dir, "space name.pdf")
	if err := os.WriteFile(pdfPath, []byte("pdf"), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	if err := os.WriteFile(txtPath, []byte("txt"), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}
	if err := os.WriteFile(unknownPath, []byte("custom"), 0o644); err != nil {
		t.Fatalf("write unknown: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(homePath), 0o755); err != nil {
		t.Fatalf("mkdir home path: %v", err)
	}
	if err := os.WriteFile(homePath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write home path: %v", err)
	}
	if err := os.WriteFile(spacedPath, []byte("spaced"), 0o644); err != nil {
		t.Fatalf("write spaced path: %v", err)
	}

	reply := strings.Join([]string{
		"这里是内联路径，不应该命中 " + pdfPath,
		pdfPath,
		"1. " + txtPath,
		"- file://" + unknownPath,
		"~/Downloads/photo.png",
		"[下载文件](" + txtPath + ")",
		"[带空格文件](<" + spacedPath + ">)",
		"file://" + pdfPath,
		filepath.Join(dir, "missing.pdf"),
		filepath.Join(dir, "folder"),
	}, "\n")

	got := extractLocalAttachmentPaths(reply)
	if len(got) != 5 {
		t.Fatalf("expected 5 paths, got %d (%v)", len(got), got)
	}
	if got[0] != pdfPath {
		t.Fatalf("got[0] = %q, want %q", got[0], pdfPath)
	}
	if got[1] != txtPath {
		t.Fatalf("got[1] = %q, want %q", got[1], txtPath)
	}
	if got[2] != unknownPath {
		t.Fatalf("got[2] = %q, want %q", got[2], unknownPath)
	}
	if got[3] != homePath {
		t.Fatalf("got[3] = %q, want %q", got[3], homePath)
	}
	if got[4] != spacedPath {
		t.Fatalf("got[4] = %q, want %q", got[4], spacedPath)
	}
}

func TestIsAllowedAttachmentPath(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	otherRoot := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(otherRoot, 0o755); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}

	allowedPath := filepath.Join(workspaceRoot, "artifacts", "report.pdf")
	deniedPath := filepath.Join(otherRoot, "report.pdf")
	if err := os.MkdirAll(filepath.Dir(allowedPath), 0o755); err != nil {
		t.Fatalf("mkdir allowed dir: %v", err)
	}
	if err := os.WriteFile(allowedPath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write allowed file: %v", err)
	}
	if err := os.WriteFile(deniedPath, []byte("no"), 0o644); err != nil {
		t.Fatalf("write denied file: %v", err)
	}

	if !isAllowedAttachmentPath(allowedPath, []string{workspaceRoot}) {
		t.Fatalf("expected %q to be allowed", allowedPath)
	}
	if isAllowedAttachmentPath(deniedPath, []string{workspaceRoot}) {
		t.Fatalf("expected %q to be denied", deniedPath)
	}
}

func TestIsAllowedAttachmentPath_ExpandsHomeAndBlocksSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	downloads := filepath.Join(home, "Downloads")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatalf("mkdir downloads: %v", err)
	}
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	inside := filepath.Join(downloads, "photo.rawout")
	if err := os.WriteFile(inside, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write inside: %v", err)
	}
	if !isAllowedAttachmentPath(inside, []string{"~/Downloads"}) {
		t.Fatalf("expected home-expanded downloads path to be allowed")
	}

	escaped := filepath.Join(downloads, "secret-link.txt")
	if err := os.Symlink(outside, escaped); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if isAllowedAttachmentPath(escaped, []string{"~/Downloads"}) {
		t.Fatalf("expected symlink escape to be denied")
	}
}

func TestDefaultUserAttachmentRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := defaultUserAttachmentRoots()
	want := []string{
		filepath.Join(home, "Documents"),
		filepath.Join(home, "Pictures"),
		filepath.Join(home, "Downloads"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("default roots = %#v, want %#v", got, want)
	}
}

func TestRewriteReplyWithAttachmentResults(t *testing.T) {
	sentPath := "/tmp/report.pdf"
	failedPath := "/tmp/archive.zip"
	reply := strings.Join([]string{
		"已生成文件：",
		sentPath,
		"这里再次内联提到 " + sentPath + "，不应该被替换。",
		failedPath,
	}, "\n")

	got := rewriteReplyWithAttachmentResults(reply, []string{sentPath}, []string{failedPath})

	if strings.Contains(got, "\n"+sentPath+"\n") {
		t.Fatalf("expected sent path line to be replaced, got %q", got)
	}
	if !strings.Contains(got, "已发送附件：report.pdf") {
		t.Fatalf("expected sent replacement, got %q", got)
	}
	if !strings.Contains(got, "这里再次内联提到 "+sentPath+"，不应该被替换。") {
		t.Fatalf("expected inline path to remain, got %q", got)
	}
	if strings.Contains(got, failedPath) {
		t.Fatalf("expected failed path to be hidden, got %q", got)
	}
	if !strings.Contains(got, "附件发送失败：archive.zip") {
		t.Fatalf("expected failure note, got %q", got)
	}
}
