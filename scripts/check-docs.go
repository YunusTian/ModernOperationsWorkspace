//go:build ignore

// check-docs 静态校验版本治理与文档漂移。
//
// 用法：
//
//	go run ./scripts/check-docs.go
//
// 覆盖 v0.5 P1「修正文档和版本治理漂移」提出的四条约束：
//
//  1. 版本一致：`VERSION` 必须等于 `sdk/version/version.go` 的 `Version =`；
//     `apps/desktop/frontend/package.json` 与 `package-lock.json`（如果存在）
//     的顶层 `"version"`；以及每个 `plugins/*/plugin.json` 的顶层 `"version"`。
//  2. 当前 tag 在 CHANGELOG 有正式章节：CHANGELOG.md 必须包含 `## [vX.Y.Z]`
//     形式的正式章节，且不允许把 `vVERSION` 的内容留在 `## [Unreleased]` 里。
//  3. README badge 与 Roadmap 不得落后于 VERSION：README 的 status badge 必须
//     引用与 VERSION 相同的版本；README roadmap 表和 docs/roadmap.md 必须
//     出现 `vVERSION` 这个字符串。
//  4. 正式 tag 的验收清单不得仍写"等待该 tag"：`docs/vVERSION-acceptance-checklist.md`
//     必须存在，且不含 "等 tag / 待远端 CI / 待 tag 触发" 等尚未发布口径的字样。
//
// 退出码：0 全部通过；非 0 至少一条失败，stderr 列出所有失败项。
//
// 不改动仓库任何文件；纯静态检查。可在本地或 CI 反复运行。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		fatal("locate repo root: %v", err)
	}

	version, err := readVersion(root)
	if err != nil {
		fatal("read VERSION: %v", err)
	}

	var errs []string
	add := func(msg string) { errs = append(errs, msg) }

	// -------- 1) 版本一致性 --------
	checkVersion(root, version, add)

	// -------- 2) CHANGELOG 章节 --------
	checkChangelog(root, version, add)

	// -------- 3) README badge / roadmap --------
	checkReadme(root, version, add)
	checkRoadmap(root, version, add)

	// -------- 4) 正式 tag 的验收清单 --------
	checkAcceptanceChecklist(root, version, add)

	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "check-docs: %d failure(s):\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
		os.Exit(1)
	}
	fmt.Printf("check-docs: OK (VERSION=%s)\n", version)
}

// repoRoot 从当前工作目录逐级向上找包含 VERSION 文件的目录。
// 允许 `go run ./scripts/check-docs.go` 从仓库根目录运行。
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "VERSION")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("VERSION not found from %s", wd)
		}
		dir = parent
	}
}

func readVersion(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(bomStrip(raw)))
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`).MatchString(v) {
		return "", fmt.Errorf("VERSION %q is not a valid semver", v)
	}
	return v, nil
}

// -----------------------------------------------------------------------------

func checkVersion(root, want string, add func(string)) {
	// 1a) sdk/version/version.go 的 Var Version = "X.Y.Z"
	src, err := os.ReadFile(filepath.Join(root, "sdk", "version", "version.go"))
	if err != nil {
		add(fmt.Sprintf("sdk/version/version.go: %v", err))
	} else {
		m := regexp.MustCompile(`(?m)^var\s+Version\s*=\s*"([^"]+)"`).FindSubmatch(src)
		switch {
		case m == nil:
			add("sdk/version/version.go: missing `var Version = \"...\"`")
		case string(m[1]) != want:
			add(fmt.Sprintf("sdk/version/version.go: Version=%q, want %q (from VERSION)", m[1], want))
		}
	}

	// 1b) apps/desktop/frontend/package.json / package-lock.json
	checkJSONVersion(root, "apps/desktop/frontend/package.json", want, add)
	if _, err := os.Stat(filepath.Join(root, "apps/desktop/frontend/package-lock.json")); err == nil {
		checkJSONVersion(root, "apps/desktop/frontend/package-lock.json", want, add)
	}

	// 1c) 所有 plugins/<id>/plugin.json 的 version
	pluginDirs, _ := filepath.Glob(filepath.Join(root, "plugins", "*", "plugin.json"))
	sort.Strings(pluginDirs)
	for _, p := range pluginDirs {
		rel, _ := filepath.Rel(root, p)
		checkJSONVersion(root, filepath.ToSlash(rel), want, add)
	}
}

func checkJSONVersion(root, rel, want string, add func(string)) {
	full := filepath.Join(root, rel)
	raw, err := os.ReadFile(full)
	if err != nil {
		add(fmt.Sprintf("%s: %v", rel, err))
		return
	}
	// 忽略 BOM
	raw = bomStrip(raw)
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		add(fmt.Sprintf("%s: parse: %v", rel, err))
		return
	}
	got, _ := obj["version"].(string)
	if got != want {
		add(fmt.Sprintf(`%s: "version"=%q, want %q (from VERSION)`, rel, got, want))
	}
}

func bomStrip(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// -----------------------------------------------------------------------------

func checkChangelog(root, version string, add func(string)) {
	full := filepath.Join(root, "CHANGELOG.md")
	raw, err := os.ReadFile(full)
	if err != nil {
		add(fmt.Sprintf("CHANGELOG.md: %v", err))
		return
	}
	text := string(raw)

	// 2a) 存在 `## [vVERSION]`（不做日期比对，只看章节头）
	header := fmt.Sprintf("## [v%s]", version)
	if !strings.Contains(text, header) {
		add(fmt.Sprintf("CHANGELOG.md: missing formal section header %q for VERSION=%s", header, version))
	}

	// 2b) `## [Unreleased]` 与下一个 `## [` 之间不允许再出现 `vVERSION` 的三级标题
	unrelIdx := strings.Index(text, "## [Unreleased]")
	if unrelIdx < 0 {
		add("CHANGELOG.md: missing `## [Unreleased]` section")
		return
	}
	// 找 Unreleased 之后紧随的下一个二级章节
	rest := text[unrelIdx+len("## [Unreleased]"):]
	nextIdx := strings.Index(rest, "\n## [")
	unrelBody := rest
	if nextIdx >= 0 {
		unrelBody = rest[:nextIdx]
	}
	// 若 Unreleased 段落里出现 `### vVERSION`，说明该内容尚未移入正式章节
	needle := fmt.Sprintf("### v%s", version)
	if strings.Contains(unrelBody, needle) {
		add(fmt.Sprintf(`CHANGELOG.md: %q must not remain under "## [Unreleased]"; move it into %q`, needle, header))
	}
}

// -----------------------------------------------------------------------------

func checkReadme(root, version string, add func(string)) {
	full := filepath.Join(root, "README.md")
	raw, err := os.ReadFile(full)
	if err != nil {
		add(fmt.Sprintf("README.md: %v", err))
		return
	}
	text := string(raw)

	// 3a) status badge：`status-vX.Y.Z_released`（shields.io 会把点/下划线做 URL 化）
	// 允许其它 badge 版本前缀存在；只要"某个 badge 引用了 vVERSION"就算通过。
	badge := regexp.MustCompile(`shields\.io/badge/status-v([0-9A-Za-z.\-]+?)_`)
	m := badge.FindStringSubmatch(text)
	switch {
	case m == nil:
		add(`README.md: cannot locate status badge matching shields.io/badge/status-vX.Y.Z_released`)
	case m[1] != version:
		add(fmt.Sprintf("README.md: status badge shows v%s, want v%s (from VERSION)", m[1], version))
	}

	// 3b) roadmap 表 / 最新交付：README 必须至少一次出现 `v<VERSION>`（当前 tag 名）
	if !strings.Contains(text, "v"+version) {
		add(fmt.Sprintf("README.md: does not mention v%s anywhere (roadmap/最新交付 应至少列出当前版本)", version))
	}
}

func checkRoadmap(root, version string, add func(string)) {
	full := filepath.Join(root, "docs", "roadmap.md")
	raw, err := os.ReadFile(full)
	if err != nil {
		add(fmt.Sprintf("docs/roadmap.md: %v", err))
		return
	}
	text := string(raw)
	if !strings.Contains(text, "v"+version) {
		add(fmt.Sprintf("docs/roadmap.md: does not mention v%s (roadmap 不得落后于 VERSION)", version))
	}
}

// -----------------------------------------------------------------------------

func checkAcceptanceChecklist(root, version string, add func(string)) {
	rel := fmt.Sprintf("docs/v%s-acceptance-checklist.md", version)
	full := filepath.Join(root, rel)
	raw, err := os.ReadFile(full)
	if err != nil {
		add(fmt.Sprintf("%s: %v (正式 tag 必须有对应验收清单)", rel, err))
		return
	}
	text := string(raw)

	// 4) 不允许留有"等待自己 tag"的口径
	forbidden := []string{
		fmt.Sprintf("等 `v%s` tag", version),
		fmt.Sprintf("等下一次打 `v%s` tag", version),
		fmt.Sprintf("待 `v%s` tag", version),
		fmt.Sprintf("待下一次 `v%s` tag", version),
		fmt.Sprintf("`v%s` tag 触发一次", version),
		fmt.Sprintf("等 `v%s` tag 触发", version),
	}
	for _, kw := range forbidden {
		if strings.Contains(text, kw) {
			add(fmt.Sprintf("%s: contains %q — 正式 tag 已发布，不应再写“等待该 tag”", rel, kw))
		}
	}
}

// -----------------------------------------------------------------------------

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "check-docs: "+format+"\n", args...)
	os.Exit(2)
}
