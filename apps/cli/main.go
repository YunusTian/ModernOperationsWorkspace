// Command mow 是 MOW 的命令行入口。
//
// 目录约定：
//
//	~/.mow/                        默认 DataDir
//	~/.mow/config.json             CLI / 全局配置
//	~/.mow/connections/targets.json  已注册目标
//	~/.mow/keys/master.key         凭据加密主密钥
//	<PluginsDir>/<id>[.exe]        插件可执行文件
//
// 子命令：
//
//	mow target add|list|rm         管理 Connection Target
//	mow run <plugin>.<cmd>         通过 Command Engine 执行
//	mow recipe list|run <id>       列出 / 执行内置 Recipe
//	mow workflow validate|run      解析并执行 Workflow YAML
package main

import (
	"fmt"
	"os"

	"github.com/mow/mow/sdk/version"
	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "mow:", err)
		os.Exit(1)
	}
}

// newRootCmd 构造根命令；所有子命令通过 App 单例访问 Core。
func newRootCmd() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:           "mow",
		Short:         "Modern Operations Workspace",
		Long:          "AI is optional. Automation is essential.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configPath, "config", "",
		"path to mow config (JSON); empty = defaults")

	// App 是所有子命令共享的运行时依赖。
	// 每个子命令的 RunE 内按需 Load()，保证 --help 等场景不做任何 IO。
	appHolder := &appHolder{configPath: &configPath}

	root.AddCommand(
		&cobra.Command{Use: "version", Short: "Print MOW version", Args: cobra.NoArgs, Run: func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), version.Version) }},
		newTargetCmd(appHolder),
		newRunCmd(appHolder),
		newRecipeCmd(appHolder),
		newWorkflowCmd(appHolder),
		newSSHCmd(appHolder),
		newAICmd(appHolder),
	)
	return root
}
