// Command mow-plugin-ssh 是 MOW 的官方 SSH 插件。
// 作为 hashicorp/go-plugin 子进程运行，向 Core 通过 gRPC 提供
// ssh.exec / ssh.upload / ssh.download 等 Command。
package main

func main() {
	// TODO: plugin.Serve(...) 注册 SSH Command / Recipe
}
