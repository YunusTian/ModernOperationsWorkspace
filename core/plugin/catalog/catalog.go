// Package catalog 实现 MOW 插件本地 Catalog（v0.5.1 雏形）。
//
// 设计要点（详见 docs/development-plan-v0.5-v1.0.md §4.3）：
//   - 静态 JSON Catalog + GitHub Release 产物
//   - 官方 Catalog + 自定义私有 Catalog URL
//   - 平台、架构、兼容版本过滤
//   - 缓存 & 离线读取（Catalog 更新失败不影响已安装插件）
//
// 该包只做 Catalog 数据抽象与拉取／缓存；下载、校验、安装归 core/plugin 主流程。
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mow/mow/sdk/manifest"
)

// CatalogVersion 是当前静态 Catalog Schema 版本。破坏性变更时递增。
const CatalogVersion = 1

// Catalog 描述一份从远端拉取或缓存中加载的静态 Catalog。
//
// 语义与 Manifest 保持一致：URL 与相对路径均不做特殊解释，交由调用方
// （安装管线）在下载阶段处理。这里只关心「有哪些插件、哪些版本、每个版本
// 有哪些平台产物」以及「静态过滤」。
type Catalog struct {
	// SchemaVersion 目前恒为 1。
	SchemaVersion int `json:"catalogVersion"`
	// Source 是该 Catalog 的来源标识（例如 "official" / "私有仓库 X"）。
	// 仅用于展示与日志，不参与业务逻辑。
	Source string `json:"source,omitempty"`
	// URL 是该 Catalog 的原始 URL。空表示来自本地文件或缓存回退。
	URL string `json:"url,omitempty"`
	// Entries 是插件条目列表。
	Entries []Entry `json:"entries"`
}

// Entry 是单个插件在 Catalog 中的静态描述。
type Entry struct {
	ID          string  `json:"id"`
	Name        string  `json:"name,omitempty"`
	Description string  `json:"description,omitempty"`
	Author      string  `json:"author,omitempty"`
	License     string  `json:"license,omitempty"`
	Homepage    string  `json:"homepage,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	// Versions 是该插件公开的版本集合，按语义化版本从高到低排列（Sort 后）。
	Versions []Release `json:"versions"`
}

// Release 描述某个版本的发布产物。
type Release struct {
	Version       string        `json:"version"`
	Yanked        bool          `json:"yanked,omitempty"`
	Compatibility Compatibility `json:"compatibility"`
	Platforms     []Artifact    `json:"platforms"`
	ReleaseNotes  string        `json:"releaseNotes,omitempty"`
	PublishedAt   string        `json:"publishedAt,omitempty"`
}

// Compatibility 与 Manifest.compatibility 同构。字段缺省 → 跳过该层校验。
type Compatibility struct {
	Core     string `json:"core,omitempty"`
	SDK      string `json:"sdk,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

// Artifact 是某个 OS/Arch 组合对应的下载产物。
type Artifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	URL      string `json:"url"`
	Checksum string `json:"checksum"`
	Size     int64  `json:"size,omitempty"`
}

// Parse 从原始 JSON 解析 Catalog。
//
// 严格模式：拒绝未知字段，避免 Catalog 静默漂移；容忍 UTF-8 BOM。
func Parse(data []byte) (*Catalog, error) {
	// 兼容 UTF-8 BOM
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var c Catalog
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("catalog: parse: %w", err)
	}
	if dec.More() {
		return nil, errors.New("catalog: trailing content after JSON document")
	}
	if c.SchemaVersion == 0 {
		return nil, errors.New("catalog: missing catalogVersion")
	}
	if c.SchemaVersion > CatalogVersion {
		return nil, fmt.Errorf("catalog: schema version %d is newer than supported %d", c.SchemaVersion, CatalogVersion)
	}
	if err := validate(&c); err != nil {
		return nil, err
	}
	c.Sort()
	return &c, nil
}

// validate 保证 Catalog 结构完整、ID 唯一、版本 semver 合法。
func validate(c *Catalog) error {
	seen := map[string]struct{}{}
	for i := range c.Entries {
		e := &c.Entries[i]
		if strings.TrimSpace(e.ID) == "" {
			return fmt.Errorf("catalog: entries[%d].id is empty", i)
		}
		if _, dup := seen[e.ID]; dup {
			return fmt.Errorf("catalog: duplicate entry id %q", e.ID)
		}
		seen[e.ID] = struct{}{}
		if len(e.Versions) == 0 {
			return fmt.Errorf("catalog: entry %q has no versions", e.ID)
		}
		versions := map[string]struct{}{}
		for j := range e.Versions {
			r := &e.Versions[j]
			if _, err := manifest.ParseVersion(r.Version); err != nil {
				return fmt.Errorf("catalog: entry %q version[%d]: %w", e.ID, j, err)
			}
			if _, dup := versions[r.Version]; dup {
				return fmt.Errorf("catalog: entry %q duplicate version %q", e.ID, r.Version)
			}
			versions[r.Version] = struct{}{}
			if len(r.Platforms) == 0 {
				return fmt.Errorf("catalog: entry %q version %q has no platforms", e.ID, r.Version)
			}
			for k := range r.Platforms {
				p := &r.Platforms[k]
				if p.OS == "" || p.Arch == "" {
					return fmt.Errorf("catalog: entry %q version %q platforms[%d]: os/arch required", e.ID, r.Version, k)
				}
				if p.URL == "" {
					return fmt.Errorf("catalog: entry %q version %q platforms[%d]: url required", e.ID, r.Version, k)
				}
				if !strings.HasPrefix(p.Checksum, "sha256:") || len(p.Checksum) != len("sha256:")+64 {
					return fmt.Errorf("catalog: entry %q version %q platforms[%d]: checksum must be sha256:<hex64>", e.ID, r.Version, k)
				}
			}
		}
	}
	return nil
}

// Sort 就地按 ID 升序 / 版本降序排列条目。Parse 会自动调用一次。
func (c *Catalog) Sort() {
	sort.Slice(c.Entries, func(i, j int) bool { return c.Entries[i].ID < c.Entries[j].ID })
	for i := range c.Entries {
		versions := c.Entries[i].Versions
		sort.SliceStable(versions, func(a, b int) bool {
			return compareVersionDesc(versions[a].Version, versions[b].Version)
		})
	}
}

// compareVersionDesc 返回 a > b（用于 sort：高版本排前）。
//
// 使用局部的 major.minor.patch 数值比较，pre-release 走字符串序：
// 有 pre-release 的版本 < 相同基版本无 pre-release，与 SemVer 2.0.0 语义一致。
func compareVersionDesc(a, b string) bool {
	return semverGreater(a, b)
}

func semverGreater(a, b string) bool {
	am, an, ap, apre := splitSemver(a)
	bm, bn, bp, bpre := splitSemver(b)
	if am != bm {
		return am > bm
	}
	if an != bn {
		return an > bn
	}
	if ap != bp {
		return ap > bp
	}
	// 相同基版本：pre-release 版本较小
	if apre == "" && bpre == "" {
		return false
	}
	if apre == "" {
		return true
	}
	if bpre == "" {
		return false
	}
	return apre > bpre
}

// splitSemver 抽取 major/minor/patch/pre-release；调用前应确保 s 已通过校验。
func splitSemver(s string) (major, minor, patch int, pre string) {
	// 去掉 build metadata
	if idx := strings.Index(s, "+"); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.Index(s, "-"); idx >= 0 {
		pre = s[idx+1:]
		s = s[:idx]
	}
	parts := strings.SplitN(s, ".", 3)
	if len(parts) == 3 {
		major, _ = atoi(parts[0])
		minor, _ = atoi(parts[1])
		patch, _ = atoi(parts[2])
	}
	return
}

func atoi(s string) (int, error) {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// Filter 描述一次静态过滤所需的条件。任一字段为空 → 跳过该层。
type Filter struct {
	// Query 是模糊搜索关键字（大小写不敏感，匹配 id / name / description / tags）。
	Query string
	// OS/Arch 用于筛掉当前平台不支持的版本。
	OS   string
	Arch string
	// CoreVersion / SDKVersion / ProtocolVersion 用于按兼容性过滤版本。
	CoreVersion     string
	SDKVersion      string
	ProtocolVersion string
	// IncludeYanked 为 true 时不过滤掉 yanked 版本。
	IncludeYanked bool
}

// Search 返回符合 filter 的 Entry 列表；每个 Entry 只保留可用的 Version。
// 若 Entry 在过滤后没有任何可用版本，则整个条目被丢弃。
func (c *Catalog) Search(filter Filter) []Entry {
	if c == nil {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(filter.Query))
	out := make([]Entry, 0, len(c.Entries))
	for _, e := range c.Entries {
		if q != "" && !entryMatchesQuery(&e, q) {
			continue
		}
		filtered := filterVersions(e.Versions, filter)
		if len(filtered) == 0 {
			continue
		}
		copy := e
		copy.Versions = filtered
		out = append(out, copy)
	}
	return out
}

// LatestFor 返回条目在当前过滤器下的最高可用版本；无匹配返回 (Release{}, false)。
func (c *Catalog) LatestFor(id string, filter Filter) (Release, bool) {
	for _, e := range c.Entries {
		if e.ID != id {
			continue
		}
		versions := filterVersions(e.Versions, filter)
		if len(versions) == 0 {
			return Release{}, false
		}
		return versions[0], true
	}
	return Release{}, false
}

func entryMatchesQuery(e *Entry, q string) bool {
	if strings.Contains(strings.ToLower(e.ID), q) {
		return true
	}
	if strings.Contains(strings.ToLower(e.Name), q) {
		return true
	}
	if strings.Contains(strings.ToLower(e.Description), q) {
		return true
	}
	for _, t := range e.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

func filterVersions(all []Release, f Filter) []Release {
	out := make([]Release, 0, len(all))
	for _, r := range all {
		if r.Yanked && !f.IncludeYanked {
			continue
		}
		if !platformSupported(r.Platforms, f.OS, f.Arch) {
			continue
		}
		if !compatibilityAllows(r.Compatibility.Core, f.CoreVersion) {
			continue
		}
		if !compatibilityAllows(r.Compatibility.SDK, f.SDKVersion) {
			continue
		}
		if !compatibilityAllows(r.Compatibility.Protocol, f.ProtocolVersion) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func platformSupported(platforms []Artifact, os, arch string) bool {
	if os == "" && arch == "" {
		return true
	}
	for _, p := range platforms {
		if os != "" && p.OS != os {
			continue
		}
		if arch != "" && p.Arch != arch {
			continue
		}
		return true
	}
	return false
}

// compatibilityAllows：constraint 或 runtime 版本任意一方为空即认为不校验该层。
func compatibilityAllows(constraint, runtimeVersion string) bool {
	if strings.TrimSpace(constraint) == "" || strings.TrimSpace(runtimeVersion) == "" {
		return true
	}
	c, err := manifest.ParseConstraint(constraint)
	if err != nil {
		return false
	}
	ok, err := c.Check(runtimeVersion)
	if err != nil {
		return false
	}
	return ok
}
