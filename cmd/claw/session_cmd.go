package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// newSessionCmd 创建 `claw session` 子命令组。
func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "管理 Agent 会话",
		Long:  "查看、清理、导出 Agent 会话历史。",
	}

	cmd.AddCommand(
		newSessionListCmd(),
		newSessionCleanCmd(),
	)

	return cmd
}

// newSessionListCmd 创建 `claw session list` 子命令。
func newSessionListCmd() *cobra.Command {
	var workDir string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出所有会话",
		Long:  "扫描 .claw/sessions/ 目录，列出所有持久化的会话。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("获取工作目录失败: %w", err)
				}
			}
			return listSessions(workDir)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVarP(&workDir, "dir", "d", "", "工作目录（默认当前目录）")

	return cmd
}

// newSessionCleanCmd 创建 `claw session clean` 子命令。
func newSessionCleanCmd() *cobra.Command {
	var workDir string
	var sessionID string

	cmd := &cobra.Command{
		Use:   "clean [session-id]",
		Short: "清理会话历史",
		Long:  "删除指定会话或所有会话的持久化文件。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("获取工作目录失败: %w", err)
				}
			}
			if len(args) > 0 {
				sessionID = args[0]
			}
			return cleanSessions(workDir, sessionID)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVarP(&workDir, "dir", "d", "", "工作目录（默认当前目录）")

	return cmd
}

// sessionEntry 表示一个会话文件的信息。
type sessionEntry struct {
	ID   string
	Size int64
	Path string
}

// listSessions 扫描并列出所有持久化会话。
func listSessions(workDir string) error {
	sessDir := filepath.Join(workDir, ".claw", "sessions")

	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("暂无会话记录")
			return nil
		}
		return fmt.Errorf("读取会话目录失败: %w", err)
	}

	if len(entries) == 0 {
		fmt.Println("暂无会话记录")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION ID\tSIZE\tPATH")
	fmt.Fprintln(w, "----------\t----\t----")

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		id := entry.Name()[:len(entry.Name())-6] // 去掉 .jsonl
		fmt.Fprintf(w, "%s\t%s\t%s\n", id, formatSize(info.Size()), filepath.Join(sessDir, entry.Name()))
	}

	w.Flush()
	return nil
}

// cleanSessions 清理会话文件。
func cleanSessions(workDir, sessionID string) error {
	sessDir := filepath.Join(workDir, ".claw", "sessions")

	if sessionID != "" {
		// 清理指定会话
		path := filepath.Join(sessDir, sessionID+".jsonl")
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("会话 %s 不存在\n", sessionID)
				return nil
			}
			return fmt.Errorf("删除会话失败: %w", err)
		}
		fmt.Printf("✅ 已删除会话: %s\n", sessionID)
		return nil
	}

	// 清理所有会话
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("暂无会话记录")
			return nil
		}
		return fmt.Errorf("读取会话目录失败: %w", err)
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(sessDir, entry.Name())
		if err := os.Remove(path); err != nil {
			fmt.Printf("⚠️ 删除 %s 失败: %v\n", entry.Name(), err)
			continue
		}
		count++
	}

	fmt.Printf("✅ 已清理 %d 个会话\n", count)
	return nil
}

// formatSize 格式化文件大小为人类可读格式。
func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
	)

	switch {
	case bytes >= MB:
		return fmt.Sprintf("%.1fMB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1fKB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
