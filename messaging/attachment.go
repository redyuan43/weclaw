package messaging

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var reMarkdownFileLink = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

func defaultAttachmentWorkspace() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Clean(os.TempDir())
	}
	return filepath.Join(home, ".weclaw", "workspace")
}

func defaultUserAttachmentRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "Documents"),
		filepath.Join(home, "Pictures"),
		filepath.Join(home, "Downloads"),
	}
}

func extractLocalAttachmentPaths(text string) []string {
	var paths []string
	seen := make(map[string]struct{})

	for _, line := range strings.Split(text, "\n") {
		for _, candidate := range attachmentCandidatesFromLine(line) {
			if !isExistingRegularFile(candidate) {
				continue
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			paths = append(paths, candidate)
		}
	}

	return paths
}

func attachmentCandidatesFromLine(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}

	var candidates []string
	for _, match := range reMarkdownFileLink.FindAllStringSubmatch(trimmed, -1) {
		if len(match) > 1 {
			if candidate := normalizeAttachmentCandidate(match[1]); candidate != "" {
				candidates = append(candidates, candidate)
			}
		}
	}
	if len(candidates) > 0 {
		return candidates
	}

	return appendCandidate(candidates, stripListMarker(trimmed))
}

func appendCandidate(candidates []string, raw string) []string {
	if candidate := normalizeAttachmentCandidate(raw); candidate != "" {
		return append(candidates, candidate)
	}
	return candidates
}

func stripListMarker(line string) string {
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}

	dot := strings.Index(line, ". ")
	if dot > 0 {
		if _, err := strconv.Atoi(line[:dot]); err == nil {
			return strings.TrimSpace(line[dot+2:])
		}
	}
	return line
}

func normalizeAttachmentCandidate(raw string) string {
	candidate := strings.TrimSpace(raw)
	candidate = strings.Trim(candidate, "`\"'")
	candidate = strings.TrimPrefix(candidate, "<")
	candidate = strings.TrimSuffix(candidate, ">")
	if candidate == "" {
		return ""
	}
	if strings.HasPrefix(candidate, "file://") {
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Path == "" {
			return ""
		}
		candidate = parsed.Path
	}
	candidate = expandHomePath(candidate)
	if !filepath.IsAbs(candidate) {
		return ""
	}
	return filepath.Clean(candidate)
}

func isExistingRegularFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func isAllowedAttachmentPath(path string, allowedRoots []string) bool {
	cleanPath, err := canonicalizePath(path, true)
	if err != nil {
		return false
	}

	for _, root := range allowedRoots {
		if root == "" {
			continue
		}
		cleanRoot, err := canonicalizePath(root, false)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(cleanRoot, cleanPath)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return true
		}
	}

	return false
}

func rewriteReplyWithAttachmentResults(reply string, sentPaths, failedPaths []string) string {
	statusByPath := make(map[string]string, len(sentPaths)+len(failedPaths))
	for _, path := range sentPaths {
		statusByPath[filepath.Clean(path)] = "已发送附件：" + filepath.Base(path)
	}
	failedSet := make(map[string]struct{}, len(failedPaths))
	for _, path := range failedPaths {
		cleanPath := filepath.Clean(path)
		statusByPath[cleanPath] = "附件发送失败：" + filepath.Base(path)
		failedSet[cleanPath] = struct{}{}
	}

	lines := strings.Split(reply, "\n")
	replacedFailures := make(map[string]struct{})
	for i, line := range lines {
		candidates := attachmentCandidatesFromLine(line)
		if len(candidates) != 1 {
			continue
		}
		candidate := filepath.Clean(candidates[0])
		if replacement, ok := statusByPath[candidate]; ok {
			lines[i] = replacement
			if _, failed := failedSet[candidate]; failed {
				replacedFailures[candidate] = struct{}{}
			}
		}
	}

	rewritten := strings.Join(lines, "\n")

	var failureLines []string
	seenFailures := make(map[string]struct{})
	for _, path := range failedPaths {
		cleanPath := filepath.Clean(path)
		if _, ok := replacedFailures[cleanPath]; ok {
			continue
		}
		if _, ok := seenFailures[cleanPath]; ok {
			continue
		}
		seenFailures[cleanPath] = struct{}{}
		failureLines = append(failureLines, "附件发送失败："+filepath.Base(path))
	}
	if len(failureLines) == 0 {
		return rewritten
	}
	if strings.TrimSpace(rewritten) == "" {
		return strings.Join(failureLines, "\n")
	}
	return rewritten + "\n" + strings.Join(failureLines, "\n")
}

func canonicalizePath(path string, mustExist bool) (string, error) {
	path = expandHomePath(path)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if realPath, err := filepath.EvalSymlinks(absPath); err == nil {
		return filepath.Clean(realPath), nil
	} else if mustExist {
		return "", err
	}
	return filepath.Clean(absPath), nil
}

func expandHomePath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
