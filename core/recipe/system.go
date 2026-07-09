// system.go 提供内置 recipe：面向常见运维查看类需求。
// 所有 v0.1 内置 recipe 一律基于 ssh.exec 组合命令，不依赖任何未落地的插件。

package recipe

import (
	"sync"
)

// Registry 是进程内的 Recipe 注册表。
type Registry struct {
	mu sync.RWMutex
	m  map[string]*Recipe
}

// NewRegistry 构造一个仅含内置 recipe 的 registry。
func NewRegistry() *Registry {
	r := &Registry{m: make(map[string]*Recipe)}
	for _, rp := range Builtins() {
		_ = r.Register(rp)
	}
	return r
}

// Register 注册一个 Recipe；重复 ID 报错。
func (r *Registry) Register(rp *Recipe) error {
	if err := rp.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[rp.ID]; ok {
		return errRecipeExists{ID: rp.ID}
	}
	r.m[rp.ID] = rp
	return nil
}

// Get 按 ID 查找 Recipe。
func (r *Registry) Get(id string) (*Recipe, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rp, ok := r.m[id]
	return rp, ok
}

// List 列出全部 Recipe。
func (r *Registry) List() []*Recipe {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Recipe, 0, len(r.m))
	for _, rp := range r.m {
		out = append(out, rp)
	}
	return out
}

type errRecipeExists struct{ ID string }

func (e errRecipeExists) Error() string { return "recipe already registered: " + e.ID }

// Builtins 返回 v0.1 内置 recipe 列表。
//
// 设计原则：
//   - 只用 ssh.exec，尽量走 POSIX 通用命令，兼容主流 Linux 发行版
//   - 输出保持"未加工"，具体解析（CPU %/磁盘容量）交由未来 v0.2 的输出映射
func Builtins() []*Recipe {
	return []*Recipe{
		{
			ID:          "system.cpu",
			Name:        "System CPU Snapshot",
			Description: "采集目标主机 CPU 使用率与负载快照",
			Steps: []Step{
				{
					ID: "loadavg", Plugin: "ssh", Command: "exec",
					Params: map[string]any{"cmd": "cat /proc/loadavg"},
				},
				{
					// top -bn1 输出首屏，包含 %Cpu(s) 行；不同发行版格式相近。
					ID: "top", Plugin: "ssh", Command: "exec",
					Params: map[string]any{"cmd": "top -bn1 | head -n 5"},
				},
			},
		},
		{
			ID:          "system.disk",
			Name:        "System Disk Usage",
			Description: "采集目标主机磁盘容量与挂载情况",
			Steps: []Step{
				{
					ID: "df", Plugin: "ssh", Command: "exec",
					Params: map[string]any{"cmd": "df -hT --output=source,fstype,size,used,avail,pcent,target 2>/dev/null || df -h"},
				},
				{
					ID: "mounts", Plugin: "ssh", Command: "exec",
					Params: map[string]any{"cmd": "mount | head -n 20"},
				},
			},
		},
	}
}
