package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// newConfigCmd 创建 `claw config` 子命令组。
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "管理配置",
		Long:  "查看和管理 go-tiny-claw 配置。",
	}

	cmd.AddCommand(
		newConfigShowCmd(),
		newConfigInitCmd(),
	)

	return cmd
}

// newConfigShowCmd 创建 `claw config show` 子命令。
func newConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "显示当前配置",
		Long:  "加载并显示当前生效的配置（合并配置文件和环境变量）。",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := DefaultConfig()

			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return fmt.Errorf("序列化配置失败: %w", err)
			}

			fmt.Println(string(data))
			return nil
		},
		SilenceUsage: true,
	}

	return cmd
}

// newConfigInitCmd 创建 `claw config init` 子命令。
func newConfigInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "生成默认配置文件",
		Long:  "在当前目录生成默认的 claw.yaml 配置文件。",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := DefaultConfig()
			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return fmt.Errorf("序列化配置失败: %w", err)
			}

			path := "claw.yaml"
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("配置文件 %s 已存在，如需覆盖请手动删除", path)
			}

			if err := os.WriteFile(path, data, 0644); err != nil {
				return fmt.Errorf("写入配置文件失败: %w", err)
			}

			fmt.Printf("✅ 配置文件已生成: %s\n", path)
			return nil
		},
		SilenceUsage: true,
	}

	return cmd
}
