package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// Manifest 数据结构
//
// 结构字段严格与 plugin.schema.json 对齐；`json` tag 与 schema 字段名一致。
// 反序列化后再由 Validate() 做业务校验。
// -----------------------------------------------------------------------------

// SupportedManifestVersion 是本包支持的 manifestVersion。v0.5.0 只支持 1。
const SupportedManifestVersion = 1

// Manifest 是 plugin.json 反序列化后的完整视图。
type Manifest struct {
	ManifestVersion int             `json:"manifestVersion"`
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Version         string          `json:"version"`
	Author          string          `json:"author,omitempty"`
	License         string          `json:"license,omitempty"`
	Homepage        string          `json:"homepage,omitempty"`
	Description     string          `json:"description,omitempty"`
	Compatibility   Compatibility   `json:"compatibility"`
	Platforms       []Platform      `json:"platforms"`
	ConnectionTypes []string        `json:"connectionTypes,omitempty"`
	Permissions     []string        `json:"permissions,omitempty"`
	Commands        []CommandRef    `json:"commands,omitempty"`
	SettingsSchema  json.RawMessage `json:"settingsSchema,omitempty"`
	Recipes         []Resource      `json:"recipes,omitempty"`
	Workflows       []Resource      `json:"workflows,omitempty"`
	DataVersion     int             `json:"dataVersion,omitempty"`
	Migrations      []Migration     `json:"migrations,omitempty"`
	Source          *Source         `json:"source,omitempty"`
	Signature       *Signature      `json:"signature,omitempty"`
}

// Compatibility 声明本插件对 Core / SDK / Protocol 三层的 semver 约束。
type Compatibility struct {
	Core     string `json:"core"`
	SDK      string `json:"sdk,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

// Platform 是一条 (os, arch) → 入口二进制映射。
type Platform struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Entrypoint string `json:"entrypoint"`
	Checksum   string `json:"checksum"`
}

// CommandRef 是 Manifest 中对 Command 的摘要（真实定义由运行时 CommandSpec 提供）。
type CommandRef struct {
	ID          string `json:"id"`
	Permission  string `json:"permission"`
	Streaming   bool   `json:"streaming,omitempty"`
	Description string `json:"description,omitempty"`
}

// Resource 是 recipes[] / workflows[] 的通用条目。
type Resource struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// Migration 描述一次数据格式升级。
type Migration struct {
	From       int    `json:"from"`
	To         int    `json:"to"`
	Entrypoint string `json:"entrypoint,omitempty"`
}

// Source 声明发布来源。
type Source struct {
	URL string `json:"url,omitempty"`
	Tag string `json:"tag,omitempty"`
}

// Signature 声明包签名。
type Signature struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

// -----------------------------------------------------------------------------
// 常量与正则
// -----------------------------------------------------------------------------

// ManifestFileName 是 Manifest 在包目录中的固定文件名。
const ManifestFileName = "plugin.json"

// idPattern 校验 id / connectionType 的合法字符集，与 JSON Schema 保持一致。
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

// commandIDPattern 允许点号与短横线，用于 command / recipe / workflow ID。
var commandIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

// semverPattern 与 schema 保持一致，允许 pre-release 与 build metadata。
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// checksumPattern 强制 SHA-256 且小写十六进制。
var checksumPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// allowedPermissions 与 sdk.Permission 语义对齐。
var allowedPermissions = map[string]struct{}{
	"read":      {},
	"write":     {},
	"execute":   {},
	"dangerous": {},
}

// allowedOS / allowedArch 与 Go GOOS/GOARCH 常见值对齐。
var allowedOS = map[string]struct{}{"linux": {}, "darwin": {}, "windows": {}}
var allowedArch = map[string]struct{}{"amd64": {}, "arm64": {}}

// -----------------------------------------------------------------------------
// Load / Parse
// -----------------------------------------------------------------------------

// Load 读取 plugin.json 并解析为 Manifest。
// path 可以是 plugin.json 文件路径，也可以是包目录（自动拼接 ManifestFileName）。
// 失败时返回带稳定错误码的 *sdk.Error。
func Load(path string) (*Manifest, error) {
	full, err := resolveManifestPath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, newError(
				ErrCodeManifestInvalid,
				fmt.Sprintf("manifest not found at %s", full),
				"", "file not found",
			)
		}
		return nil, newError(
			ErrCodeManifestInvalid,
			fmt.Sprintf("read manifest: %v", err),
			"", err.Error(),
		)
	}
	return Parse(data)
}

// Parse 直接从字节反序列化并校验 Manifest。
func Parse(data []byte) (*Manifest, error) {
	if len(data) == 0 {
		return nil, newError(ErrCodeManifestInvalid, "empty manifest", "", "empty input")
	}
	m := &Manifest{}
	dec := json.NewDecoder(newTrimReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(m); err != nil {
		return nil, newError(
			ErrCodeManifestInvalid,
			fmt.Sprintf("decode manifest: %v", err),
			"", err.Error(),
		)
	}
	// 拒绝存在多个顶层 JSON 值的情况。
	if dec.More() {
		return nil, newError(
			ErrCodeManifestInvalid,
			"manifest contains trailing content after root object",
			"", "trailing content",
		)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// resolveManifestPath 支持传入包目录或 plugin.json 文件路径。
func resolveManifestPath(path string) (string, error) {
	if path == "" {
		return "", newError(ErrCodeManifestInvalid, "empty manifest path", "", "empty path")
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", newError(
				ErrCodeManifestInvalid,
				fmt.Sprintf("manifest path not found: %s", path),
				"", "file not found",
			)
		}
		return "", newError(
			ErrCodeManifestInvalid,
			fmt.Sprintf("stat manifest: %v", err),
			"", err.Error(),
		)
	}
	if info.IsDir() {
		return filepath.Join(path, ManifestFileName), nil
	}
	return path, nil
}

// newTrimReader 允许 Manifest 顶部 BOM / 前导空白，避免 Windows 记事本类工具产生的兼容问题。
func newTrimReader(data []byte) io.Reader {
	// 去掉 UTF-8 BOM
	trimmed := data
	if len(trimmed) >= 3 && trimmed[0] == 0xEF && trimmed[1] == 0xBB && trimmed[2] == 0xBF {
		trimmed = trimmed[3:]
	}
	return strings.NewReader(string(trimmed))
}

// -----------------------------------------------------------------------------
// Validate
//
// Validate 只做结构与语义校验，不涉及文件系统（checksum / entrypoint 是否存在
// 由 `mow plugin validate` 命令另行校验，避免本包依赖磁盘）。
// -----------------------------------------------------------------------------

// Validate 对 Manifest 做完整业务校验，返回带稳定错误码的 *sdk.Error。
// 所有校验失败均使用 ErrCodeManifestInvalid，Details.field 精确到字段。
func (m *Manifest) Validate() error {
	if m == nil {
		return newError(ErrCodeManifestInvalid, "manifest is nil", "", "nil manifest")
	}

	if m.ManifestVersion != SupportedManifestVersion {
		return newError(
			ErrCodeManifestInvalid,
			fmt.Sprintf("unsupported manifestVersion=%d, expected %d", m.ManifestVersion, SupportedManifestVersion),
			"manifestVersion", "unsupported version",
		)
	}
	if !idPattern.MatchString(m.ID) {
		return newError(ErrCodeManifestInvalid, "invalid plugin id", "id", "must match ^[a-z][a-z0-9_-]{1,63}$")
	}
	if strings.TrimSpace(m.Name) == "" {
		return newError(ErrCodeManifestInvalid, "name is required", "name", "empty name")
	}
	if !semverPattern.MatchString(m.Version) {
		return newError(ErrCodeManifestInvalid, "invalid version", "version", "not semver")
	}

	if err := m.validateCompatibility(); err != nil {
		return err
	}
	if err := m.validatePlatforms(); err != nil {
		return err
	}
	if err := m.validateConnectionTypes(); err != nil {
		return err
	}
	if err := m.validatePermissions(); err != nil {
		return err
	}
	if err := m.validateCommands(); err != nil {
		return err
	}
	if err := m.validateResources("recipes", m.Recipes); err != nil {
		return err
	}
	if err := m.validateResources("workflows", m.Workflows); err != nil {
		return err
	}
	if err := m.validateMigrations(); err != nil {
		return err
	}
	if err := m.validateSignature(); err != nil {
		return err
	}
	return nil
}

func (m *Manifest) validateCompatibility() error {
	if strings.TrimSpace(m.Compatibility.Core) == "" {
		return newError(ErrCodeManifestInvalid, "compatibility.core is required", "compatibility.core", "empty")
	}
	if _, err := ParseConstraint(m.Compatibility.Core); err != nil {
		return newError(ErrCodeManifestInvalid, fmt.Sprintf("compatibility.core: %v", err), "compatibility.core", err.Error())
	}
	if m.Compatibility.SDK != "" {
		if _, err := ParseConstraint(m.Compatibility.SDK); err != nil {
			return newError(ErrCodeManifestInvalid, fmt.Sprintf("compatibility.sdk: %v", err), "compatibility.sdk", err.Error())
		}
	}
	if m.Compatibility.Protocol != "" {
		if _, err := ParseConstraint(m.Compatibility.Protocol); err != nil {
			return newError(ErrCodeManifestInvalid, fmt.Sprintf("compatibility.protocol: %v", err), "compatibility.protocol", err.Error())
		}
	}
	return nil
}

func (m *Manifest) validatePlatforms() error {
	if len(m.Platforms) == 0 {
		return newError(ErrCodeManifestInvalid, "platforms must contain at least one entry", "platforms", "empty")
	}
	seen := map[string]int{}
	for i, p := range m.Platforms {
		field := fmt.Sprintf("platforms[%d]", i)
		if _, ok := allowedOS[p.OS]; !ok {
			return newError(ErrCodeManifestInvalid, fmt.Sprintf("unsupported os %q", p.OS), field+".os", "unsupported os")
		}
		if _, ok := allowedArch[p.Arch]; !ok {
			return newError(ErrCodeManifestInvalid, fmt.Sprintf("unsupported arch %q", p.Arch), field+".arch", "unsupported arch")
		}
		if strings.TrimSpace(p.Entrypoint) == "" {
			return newError(ErrCodeManifestInvalid, "entrypoint is required", field+".entrypoint", "empty")
		}
		if strings.HasPrefix(p.Entrypoint, "/") || strings.Contains(p.Entrypoint, "..") || strings.Contains(p.Entrypoint, `\`) {
			return newError(ErrCodeManifestInvalid, "entrypoint must be a relative package-local path", field+".entrypoint", "must be relative, no .. and no backslash")
		}
		if !checksumPattern.MatchString(p.Checksum) {
			return newError(ErrCodeManifestInvalid, "checksum must be sha256:<hex64>", field+".checksum", "invalid checksum format")
		}
		key := p.OS + "/" + p.Arch
		if prev, dup := seen[key]; dup {
			return newError(ErrCodeManifestInvalid,
				fmt.Sprintf("duplicate platform %s (also at platforms[%d])", key, prev),
				field, "duplicate os/arch")
		}
		seen[key] = i
	}
	return nil
}

func (m *Manifest) validateConnectionTypes() error {
	seen := map[string]struct{}{}
	for i, ct := range m.ConnectionTypes {
		field := fmt.Sprintf("connectionTypes[%d]", i)
		if !idPattern.MatchString(ct) {
			return newError(ErrCodeManifestInvalid, "invalid connection type", field, "must match id pattern")
		}
		if _, dup := seen[ct]; dup {
			return newError(ErrCodeManifestInvalid, "duplicate connection type", field, "duplicate")
		}
		seen[ct] = struct{}{}
	}
	return nil
}

func (m *Manifest) validatePermissions() error {
	seen := map[string]struct{}{}
	for i, p := range m.Permissions {
		field := fmt.Sprintf("permissions[%d]", i)
		if _, ok := allowedPermissions[p]; !ok {
			return newError(ErrCodeManifestInvalid,
				fmt.Sprintf("unknown permission %q", p), field, "not in allowed set")
		}
		if _, dup := seen[p]; dup {
			return newError(ErrCodeManifestInvalid, "duplicate permission", field, "duplicate")
		}
		seen[p] = struct{}{}
	}
	return nil
}

func (m *Manifest) validateCommands() error {
	seen := map[string]int{}
	for i, c := range m.Commands {
		field := fmt.Sprintf("commands[%d]", i)
		if !commandIDPattern.MatchString(c.ID) {
			return newError(ErrCodeManifestInvalid, "invalid command id", field+".id", "must match command id pattern")
		}
		if _, ok := allowedPermissions[c.Permission]; !ok {
			return newError(ErrCodeManifestInvalid,
				fmt.Sprintf("unknown permission %q", c.Permission),
				field+".permission", "not in allowed set")
		}
		if prev, dup := seen[c.ID]; dup {
			return newError(ErrCodeManifestInvalid,
				fmt.Sprintf("duplicate command id %q (also at commands[%d])", c.ID, prev),
				field+".id", "duplicate")
		}
		seen[c.ID] = i
	}
	return nil
}

func (m *Manifest) validateResources(kind string, items []Resource) error {
	seenID := map[string]int{}
	seenPath := map[string]int{}
	for i, r := range items {
		field := fmt.Sprintf("%s[%d]", kind, i)
		if !commandIDPattern.MatchString(r.ID) {
			return newError(ErrCodeManifestInvalid,
				fmt.Sprintf("invalid %s id", kind), field+".id", "must match id pattern")
		}
		if strings.TrimSpace(r.Path) == "" {
			return newError(ErrCodeManifestInvalid,
				fmt.Sprintf("%s path is required", kind), field+".path", "empty")
		}
		if strings.HasPrefix(r.Path, "/") || strings.Contains(r.Path, "..") || strings.Contains(r.Path, `\`) {
			return newError(ErrCodeManifestInvalid,
				fmt.Sprintf("%s path must be relative", kind), field+".path", "must be relative, no .. and no backslash")
		}
		if prev, dup := seenID[r.ID]; dup {
			return newError(ErrCodeManifestInvalid,
				fmt.Sprintf("duplicate %s id %q (also at %s[%d])", kind, r.ID, kind, prev),
				field+".id", "duplicate")
		}
		if prev, dup := seenPath[r.Path]; dup {
			return newError(ErrCodeManifestInvalid,
				fmt.Sprintf("duplicate %s path %q (also at %s[%d])", kind, r.Path, kind, prev),
				field+".path", "duplicate")
		}
		seenID[r.ID] = i
		seenPath[r.Path] = i
	}
	return nil
}

func (m *Manifest) validateMigrations() error {
	for i, mg := range m.Migrations {
		field := fmt.Sprintf("migrations[%d]", i)
		if mg.From < 0 {
			return newError(ErrCodeManifestInvalid, "migration.from must be >= 0", field+".from", "negative")
		}
		if mg.To <= mg.From {
			return newError(ErrCodeManifestInvalid, "migration.to must be > migration.from", field+".to", "to <= from")
		}
	}
	return nil
}

func (m *Manifest) validateSignature() error {
	if m.Signature == nil {
		return nil
	}
	switch m.Signature.Algorithm {
	case "sigstore", "minisign":
	default:
		return newError(ErrCodeManifestInvalid,
			fmt.Sprintf("unknown signature algorithm %q", m.Signature.Algorithm),
			"signature.algorithm", "not in allowed set")
	}
	if strings.TrimSpace(m.Signature.Value) == "" {
		return newError(ErrCodeManifestInvalid, "signature.value is required", "signature.value", "empty")
	}
	return nil
}

// -----------------------------------------------------------------------------
// 便捷方法
// -----------------------------------------------------------------------------

// PlatformFor 返回匹配 (os, arch) 的 Platform；找不到时返回 nil。
func (m *Manifest) PlatformFor(goos, goarch string) *Platform {
	if m == nil {
		return nil
	}
	for i := range m.Platforms {
		p := &m.Platforms[i]
		if p.OS == goos && p.Arch == goarch {
			return p
		}
	}
	return nil
}

// MatchMetadata 校验 Manifest 与运行时 sdk.Metadata 是否一致。
//
// v0.5.0 仅校验 id 与 version 两个关键字段；未来可扩展 commands / permissions
// 摘要一致性。失败时返回带 ErrCodeManifestMismatch 的 sdk.Error，Details 包含：
//
//	"field":    "id" | "version"
//	"manifest": Manifest 声明值
//	"runtime":  运行时实际值
func (m *Manifest) MatchMetadata(meta sdk.Metadata) error {
	if m == nil {
		return newError(ErrCodeManifestMismatch, "manifest is nil", "", "nil manifest")
	}
	if m.ID != meta.ID {
		return mismatch("id", m.ID, meta.ID)
	}
	if m.Version != meta.Version {
		return mismatch("version", m.Version, meta.Version)
	}
	return nil
}

func mismatch(field, manifestVal, runtimeVal string) *sdk.Error {
	e := newError(
		ErrCodeManifestMismatch,
		fmt.Sprintf("manifest %s %q does not match runtime %q", field, manifestVal, runtimeVal),
		field, "value mismatch",
	)
	return e.WithDetails(map[string]any{
		"field":    field,
		"manifest": manifestVal,
		"runtime":  runtimeVal,
	})
}
