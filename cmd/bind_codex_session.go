package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/weclaw/codexsession"
	"github.com/spf13/cobra"
)

var bindCodexSessionCwd string

func init() {
	bindCodexSessionCmd.Flags().StringVar(&bindCodexSessionCwd, "cwd", "", "Project directory used to match Codex sessions (defaults to current directory)")
	rootCmd.AddCommand(bindCodexSessionCmd)
}

var bindCodexSessionCmd = &cobra.Command{
	Use:   "bind-codex-session",
	Short: "Bind the current project to a saved Codex session",
	RunE:  runBindCodexSession,
}

func runBindCodexSession(cmd *cobra.Command, args []string) error {
	projectCwd := strings.TrimSpace(bindCodexSessionCwd)
	if projectCwd == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		projectCwd = cwd
	}

	projectCwd, err := codexsession.NormalizeCwd(projectCwd)
	if err != nil {
		return err
	}

	sessions, err := codexsession.FindSessionsByCwd(projectCwd)
	if err != nil {
		return err
	}

	fmt.Printf("项目目录: %s\n", projectCwd)
	if len(sessions) == 0 {
		if err := codexsession.ClearBinding(projectCwd); err != nil {
			return err
		}
		fmt.Println("没有找到这个目录下的 Codex session。")
		fmt.Println("weclaw 将在首次收到消息时自动创建新的 Codex 上下文。")
		return nil
	}

	existing, err := codexsession.LoadBinding(projectCwd)
	if err != nil {
		return err
	}

	if len(sessions) == 1 {
		selected := sessions[0]
		if err := codexsession.SaveBinding(bindingFromSession(selected)); err != nil {
			return err
		}
		fmt.Printf("已自动绑定唯一的 Codex session: %s\n", selected.ThreadID)
		if selected.Preview != "" {
			fmt.Printf("预览: %s\n", selected.Preview)
		}
		return nil
	}

	defaultIndex := defaultSelectionIndex(existing, sessions)
	fmt.Printf("找到 %d 个 Codex session，请选择要恢复的上下文：\n", len(sessions))
	for i, session := range sessions {
		marker := " "
		if existing != nil && existing.ThreadID == session.ThreadID {
			marker = "*"
		}
		preview := session.Preview
		if preview == "" {
			preview = "(无可用预览)"
		}
		fmt.Printf("  [%d]%s %s  %s\n", i+1, marker, formatSessionTimestamp(session), preview)
		fmt.Printf("      id=%s  source=%s\n", session.ThreadID, formatSessionSource(session))
	}
	fmt.Printf("直接回车默认选择 [%d]，输入 0 跳过绑定: ", defaultIndex)

	selection, err := readSelection(os.Stdin, defaultIndex, len(sessions))
	if err != nil {
		return err
	}
	if selection == 0 {
		if err := codexsession.ClearBinding(projectCwd); err != nil {
			return err
		}
		fmt.Println("已跳过绑定，weclaw 将在首次消息时新建上下文。")
		return nil
	}

	selected := sessions[selection-1]
	if err := codexsession.SaveBinding(bindingFromSession(selected)); err != nil {
		return err
	}
	fmt.Printf("已绑定 Codex session: %s\n", selected.ThreadID)
	if selected.Preview != "" {
		fmt.Printf("预览: %s\n", selected.Preview)
	}
	return nil
}

func bindingFromSession(session codexsession.Session) codexsession.Binding {
	return codexsession.Binding{
		ProjectCwd:  session.ProjectCwd,
		ThreadID:    session.ThreadID,
		SessionFile: session.FilePath,
		Source:      session.Source,
		Originator:  session.Originator,
		Preview:     session.Preview,
	}
}

func defaultSelectionIndex(existing *codexsession.Binding, sessions []codexsession.Session) int {
	if existing != nil {
		for i, session := range sessions {
			if session.ThreadID == existing.ThreadID {
				return i + 1
			}
		}
	}
	return 1
}

func readSelection(r io.Reader, defaultIndex, maxIndex int) (int, error) {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultIndex, nil
		}

		value, convErr := strconv.Atoi(line)
		if convErr == nil && value >= 0 && value <= maxIndex {
			return value, nil
		}

		if err == io.EOF {
			return 0, fmt.Errorf("invalid selection %q", line)
		}
		fmt.Printf("请输入 0 到 %d 之间的编号: ", maxIndex)
	}
}

func formatSessionTimestamp(session codexsession.Session) string {
	if session.Timestamp.IsZero() {
		return "(未知时间)"
	}
	return session.Timestamp.Local().Format("2006-01-02 15:04:05")
}

func formatSessionSource(session codexsession.Session) string {
	source := strings.TrimSpace(session.Source)
	originator := strings.TrimSpace(session.Originator)
	switch {
	case source != "" && originator != "":
		return source + "/" + originator
	case source != "":
		return source
	case originator != "":
		return originator
	default:
		return "unknown"
	}
}
