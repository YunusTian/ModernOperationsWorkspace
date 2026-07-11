package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// -----------------------------------------------------------------------------
// 包内文件系统校验（Package Validation）
//
// Manifest 的静态字段校验由 Validate() 完成，本文件在此之上补齐依赖磁盘的检查：
//
//   - 每个 platforms[].entrypoint 在包内实际存在
//   - 每个 platforms[].entrypoint 的 SHA-256 与 Manifest 声明匹配
//   - 每个 recipes[].path / workflows[].path 在包内实际存在
//
// 供 `mow plugin validate` 命令与后续 install / upgrade 复用。
// -----------------------------------------------------------------------------

// PackageCheck 是单条文件系统级校验结果。
type PackageCheck struct {
	// Kind 描述被校验的对象类别："entrypoint" / "recipe" / "workflow" / "checksum"。
	Kind string
	// Path 是包内相对路径。
	Path string
	// OK 表示是否通过。
	OK bool
	// Err 是 OK=false 时的错误，携带稳定错误码（*sdk.Error）。
	Err error
}

// PackageReport 汇总一次 ValidatePackage 的结果。
type PackageReport struct {
	// PackageDir 是被校验的包根目录（绝对路径）。
	PackageDir string
	// Checks 按 platforms / recipes / workflows 顺序追加。
	Checks []PackageCheck
	// FirstErr 是第一个失败项的错误，便于快速判定整体结果。
	FirstErr error
}

// OK 表示 report 中所有 check 均通过。
func (r *PackageReport) OK() bool { return r != nil && r.FirstErr == nil }

// ValidatePackage 对包目录做完整的静态 + 文件系统校验。
//
// 步骤：
//  1. Load(packageDir) 读取并做 Manifest.Validate()
//  2. 遍历 platforms：文件存在 → 计算 SHA-256 → 与 Manifest.Checksum 比对
//  3. 遍历 recipes / workflows：仅校验路径存在
//
// 任一环节失败都会写入 Checks 中，且继续检查后续项，最后返回 report 与首个错误。
// 若 Load / Validate 阶段失败则直接返回 (nil, err)（此时无 report）。
func ValidatePackage(packageDir string) (*PackageReport, error) {
	m, err := Load(packageDir)
	if err != nil {
		return nil, err
	}
	absDir, absErr := filepath.Abs(packageDir)
	if absErr != nil {
		absDir = packageDir
	}
	// packageDir 可能是 plugin.json 文件路径，此时以其父目录为包根。
	if info, statErr := os.Stat(absDir); statErr == nil && !info.IsDir() {
		absDir = filepath.Dir(absDir)
	}

	report := &PackageReport{PackageDir: absDir}

	for i, p := range m.Platforms {
		field := fmt.Sprintf("platforms[%d]", i)
		entry := filepath.Join(absDir, filepath.FromSlash(p.Entrypoint))

		// 1) entrypoint 存在？
		info, statErr := os.Stat(entry)
		if statErr != nil {
			report.appendErr(PackageCheck{Kind: "entrypoint", Path: p.Entrypoint},
				entrypointMissing(field+".entrypoint", p.Entrypoint, statErr))
			// entrypoint 缺失，跳过 checksum
			continue
		}
		if info.IsDir() {
			report.appendErr(PackageCheck{Kind: "entrypoint", Path: p.Entrypoint},
				entrypointMissing(field+".entrypoint", p.Entrypoint,
					errors.New("entrypoint is a directory")))
			continue
		}
		report.append(PackageCheck{Kind: "entrypoint", Path: p.Entrypoint, OK: true})

		// 2) checksum 匹配？
		actual, sumErr := hashFileSHA256(entry)
		if sumErr != nil {
			report.appendErr(PackageCheck{Kind: "checksum", Path: p.Entrypoint},
				newError(ErrCodeChecksumMismatch,
					fmt.Sprintf("compute sha256 for %s: %v", p.Entrypoint, sumErr),
					field+".checksum", sumErr.Error()))
			continue
		}
		if !strings.EqualFold(actual, p.Checksum) {
			report.appendErr(PackageCheck{Kind: "checksum", Path: p.Entrypoint},
				checksumMismatch(field+".checksum", p.Entrypoint, p.Checksum, actual))
			continue
		}
		report.append(PackageCheck{Kind: "checksum", Path: p.Entrypoint, OK: true})
	}

	for i, r := range m.Recipes {
		field := fmt.Sprintf("recipes[%d]", i)
		checkResourceExists(report, absDir, "recipe", field, r.Path)
	}
	for i, w := range m.Workflows {
		field := fmt.Sprintf("workflows[%d]", i)
		checkResourceExists(report, absDir, "workflow", field, w.Path)
	}

	return report, report.FirstErr
}

func checkResourceExists(report *PackageReport, absDir, kind, field, rel string) {
	full := filepath.Join(absDir, filepath.FromSlash(rel))
	if _, err := os.Stat(full); err != nil {
		report.appendErr(PackageCheck{Kind: kind, Path: rel},
			entrypointMissing(field+".path", rel, err))
		return
	}
	report.append(PackageCheck{Kind: kind, Path: rel, OK: true})
}

func (r *PackageReport) append(c PackageCheck) {
	r.Checks = append(r.Checks, c)
}

func (r *PackageReport) appendErr(c PackageCheck, err error) {
	c.OK = false
	c.Err = err
	r.Checks = append(r.Checks, c)
	if r.FirstErr == nil {
		r.FirstErr = err
	}
}

// hashFileSHA256 返回 "sha256:<hex64>" 形式的 checksum，便于与 Manifest 直接比对。
func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func entrypointMissing(field, path string, cause error) error {
	e := newError(
		ErrCodeEntrypointMissing,
		fmt.Sprintf("path %q not found: %v", path, cause),
		field, cause.Error(),
	)
	return e.WithDetails(map[string]any{"path": path})
}

func checksumMismatch(field, path, expected, actual string) error {
	e := newError(
		ErrCodeChecksumMismatch,
		fmt.Sprintf("checksum mismatch for %s", path),
		field, "sha256 mismatch",
	)
	return e.WithDetails(map[string]any{
		"path":     path,
		"expected": expected,
		"actual":   actual,
	})
}
