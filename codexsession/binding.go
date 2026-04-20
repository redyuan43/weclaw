package codexsession

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxScannerTokenSize = 4 * 1024 * 1024
	defaultPreviewLimit = 96
)

// Session describes a persisted Codex session discovered from ~/.codex/sessions.
type Session struct {
	ThreadID   string
	ProjectCwd string
	Timestamp  time.Time
	Source     string
	Originator string
	FilePath   string
	Preview    string
}

// Binding stores the selected Codex thread for a project directory.
type Binding struct {
	ProjectCwd  string `json:"project_cwd"`
	ThreadID    string `json:"thread_id"`
	SessionFile string `json:"session_file,omitempty"`
	Source      string `json:"source,omitempty"`
	Originator  string `json:"originator,omitempty"`
	Preview     string `json:"preview,omitempty"`
	BoundAt     string `json:"bound_at,omitempty"`
}

type bindingFile struct {
	Projects map[string]Binding `json:"projects"`
}

type sessionEnvelope struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ID         string `json:"id"`
	Timestamp  string `json:"timestamp"`
	Cwd        string `json:"cwd"`
	Source     string `json:"source"`
	Originator string `json:"originator"`
}

type responseItemMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// NormalizeCwd returns a stable absolute path for project matching.
func NormalizeCwd(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty cwd")
	}
	if !filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = absPath
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != "" {
		path = resolved
	}
	return filepath.Clean(path), nil
}

// FindSessionsByCwd returns all Codex sessions whose persisted cwd exactly matches the target path.
func FindSessionsByCwd(projectCwd string) ([]Session, error) {
	target, err := NormalizeCwd(projectCwd)
	if err != nil {
		return nil, err
	}

	root, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	sessions := make([]Session, 0, 8)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}

		session, err := readSessionMeta(path)
		if err != nil {
			return nil
		}
		if session.ProjectCwd != target {
			return nil
		}

		session.Preview = readSessionPreview(path)
		sessions = append(sessions, session)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].Timestamp.Equal(sessions[j].Timestamp) {
			sessions[i].FilePath > sessions[j].FilePath
		}
		return sessions[i].Timestamp.After(sessions[j].Timestamp)
	})
	return sessions, nil
}

// LoadBinding loads the saved thread binding for the given project directory.
func LoadBinding(projectCwd string) (*Binding, error) {
	target, err := NormalizeCwd(projectCwd)
	if err != nil {
		return nil, err
	}

	data, err := loadBindingFile()
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}

	binding, ok := data.Projects[target]
	if !ok {
		return nil, nil
	}
	binding.ProjectCwd = target
	return &binding, nil
}

// SaveBinding persists the selected Codex thread for the project directory.
func SaveBinding(binding Binding) error {
	target, err := NormalizeCwd(binding.ProjectCwd)
	if err != nil {
		return err
	}
	if strings.TrimSpace(binding.ThreadID) == "" {
		return fmt.Errorf("empty thread id")
	}

	data, err := loadBindingFile()
	if err != nil {
		return err
	}
	if data == nil {
		data = &bindingFile{Projects: make(map[string]Binding)}
	}
	if data.Projects == nil {
		data.Projects = make(map[string]Binding)
	}

	binding.ProjectCwd = target
	if binding.BoundAt == "" {
		binding.BoundAt = time.Now().UTC().Format(time.RFC3339)
	}
	data.Projects[target] = binding
	return writeBindingFile(data)
}

// ClearBinding removes any saved binding for the given project directory.
func ClearBinding(projectCwd string) error {
	target, err := NormalizeCwd(projectCwd)
	if err != nil {
		return err
	}

	data, err := loadBindingFile()
	if err != nil {
		return err
	}
	if data == nil || len(data.Projects) == 0 {
		return nil
	}
	delete(data.Projects, target)
	return writeBindingFile(data)
}

func loadBindingFile() (*bindingFile, error) {
	path, err := bindingPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var data bindingFile
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse binding file: %w", err)
	}
	if data.Projects == nil {
		data.Projects = make(map[string]Binding)
	}
	return &data, nil
}

func writeBindingFile(data *bindingFile) error {
	path, err := bindingPath()
	if err != nil {
		return err
	}
	if data == nil {
		data = &bindingFile{Projects: make(map[string]Binding)}
	}
	if data.Projects == nil {
		data.Projects = make(map[string]Binding)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o600)
}

func bindingPath() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("WECLAW_CODEX_BINDINGS_PATH")); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "codex-session-bindings.json"), nil
}

func sessionsDir() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("WECLAW_CODEX_SESSIONS_DIR")); custom != "" {
		return custom, nil
	}
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(codexHome, "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

func readSessionMeta(path string) (Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScannerTokenSize)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Session{}, err
		}
		return Session{}, io.EOF
	}

	var envelope sessionEnvelope
	if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
		return Session{}, err
	}
	if envelope.Type != "session_meta" {
		return Session{}, fmt.Errorf("missing session_meta")
	}

	var payload sessionMetaPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return Session{}, err
	}

	projectCwd, err := NormalizeCwd(payload.Cwd)
	if err != nil {
		return Session{}, err
	}

	return Session{
		ThreadID:   strings.TrimSpace(payload.ID),
		ProjectCwd: projectCwd,
		Timestamp:  parseSessionTime(payload.Timestamp, envelope.Timestamp),
		Source:     strings.TrimSpace(payload.Source),
		Originator: strings.TrimSpace(payload.Originator),
		FilePath:   path,
	}, nil
}

func readSessionPreview(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScannerTokenSize)

	var preview string
	for scanner.Scan() {
		var envelope sessionEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			continue
		}
		if envelope.Type != "response_item" {
			continue
		}

		var message responseItemMessage
		if err := json.Unmarshal(envelope.Payload, &message); err != nil {
			continue
		}
		if message.Type != "message" || message.Role != "user" {
			continue
		}

		text := extractPreviewText(message)
		if text == "" || looksInternalMessage(text) {
			continue
		}
		preview = truncatePreview(text, defaultPreviewLimit)
	}
	return preview
}

func extractPreviewText(message responseItemMessage) string {
	parts := make([]string, 0, len(message.Content))
	for _, item := range message.Content {
		if item.Type != "input_text" {
			continue
		}
		text := strings.TrimSpace(item.Text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func looksInternalMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(trimmed, "<environment_context>") || strings.HasPrefix(lower, "# agents.md instructions")
}

func truncatePreview(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func parseSessionTime(values ...string) time.Time {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
