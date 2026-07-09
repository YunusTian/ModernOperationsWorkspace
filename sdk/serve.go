package sdk

import "errors"

// -----------------------------------------------------------------------------
// Serve：插件进程入口
// -----------------------------------------------------------------------------

// Serve 是插件进程的推荐入口。它会：
//   1. 校验 Plugin 元信息（ID / Version / 权限声明）
//   2. 启动 hashicorp/go-plugin gRPC 服务
//   3. 阻塞直到 Core 发出 Shutdown 或进程收到终止信号
//
// 典型使用：
//
//	func main() {
//		sdk.Serve(&MyPlugin{})
//	}
//
// 若校验失败，Serve 会通过 os.Exit(1) 并向 stderr 打印错误。
//
// 注意：真正的 gRPC 桥接实现位于 sdk/internal/grpcbridge（后续引入）。
// 本函数当前只做静态校验，桥接层就绪后会补齐。
func Serve(p Plugin) {
	if err := Validate(p); err != nil {
		panic(err) // 由内部 recover 转换为 os.Exit(1)，此处占位
	}
	// TODO(sdk): 接入 hashicorp/go-plugin 与 sdk/proto 生成物。
	// 目前保持 API 稳定，插件代码可以先写起来。
}

// Validate 对插件的静态定义做完整性校验。
// 建议在单元测试中主动调用，尽早暴露错误。
func Validate(p Plugin) error {
	if p == nil {
		return errors.New("sdk: plugin is nil")
	}

	m := p.Metadata()
	if m.ID == "" {
		return errors.New("sdk: metadata.ID is required")
	}
	if m.Name == "" {
		return errors.New("sdk: metadata.Name is required")
	}
	if m.Version == "" {
		return errors.New("sdk: metadata.Version is required")
	}

	seen := map[string]struct{}{}
	for _, h := range p.Commands() {
		if h == nil {
			return errors.New("sdk: nil CommandHandler")
		}
		spec := h.Spec()
		if spec.ID == "" {
			return errors.New("sdk: command.ID is required")
		}
		if _, dup := seen[spec.ID]; dup {
			return errors.New("sdk: duplicate command id: " + spec.ID)
		}
		seen[spec.ID] = struct{}{}

		if spec.Permission == PermUnspecified {
			return errors.New("sdk: command must declare permission: " + spec.ID)
		}
	}

	return nil
}
