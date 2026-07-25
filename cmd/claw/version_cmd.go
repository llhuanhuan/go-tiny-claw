package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// newVersionCmd 创建 `claw version` 子命令。
func newVersionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "显示版本信息",
		Long:  "显示 go-tiny-claw 的版本号、编译信息和运行环境。",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("go-tiny-claw %s\n", version)
			fmt.Printf("  commit:    %s\n", commit)
			fmt.Printf("  go:        %s\n", runtime.Version())
			fmt.Printf("  os/arch:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
		},
	}

	return cmd
}
