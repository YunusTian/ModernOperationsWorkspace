//go:build ignore

// build-catalog 生成官方 Catalog (catalog.json)。
//
// 用法（release workflow 使用）：
//
//	go run ./scripts/build-catalog.go \
//	    -artifacts ./artifacts \
//	    -version v0.5.1 \
//	    -base-url "https://github.com/mow/mow/releases/download/v0.5.1" \
//	    -plugins ssh,docker,ai,pve \
//	    -source official \
//	    -out ./artifacts/catalog.json
//
// 输入：
//   - -plugins：插件 id 列表，逗号分隔。每个 id 对应 plugins/<id>/plugin.json 提供
//     元信息（name / description / author / compatibility.core 等）
//   - -artifacts：release 打好的 tar.gz 目录；文件命名遵循 release.yml：
//     mow-<id>-plugin-<os>-<arch>.tar.gz
//   - -base-url：下游用户下载时的 URL 前缀（catalog 里的每个平台 url 由此拼出）
//   - -version：本次 release 的完整 tag（含 v 前缀或 rc 后缀均可）
//   - -source：写入 catalog.source 字段（默认 "official"）
//
// 输出：
//   - -out：一份 catalog.json，可直接被 core/plugin/catalog.Parse 解析。
//
// 约束：
//   - 每个平台产物必须存在于 -artifacts 目录，否则报错
//   - 每个产物按 SHA-256 计算校验和，与 tar.gz 一一对应
//   - 产物 URL = <base-url>/<file-name>
//   - 每个 plugin.json 里 platforms[] 列出的 OS/Arch 都必须在 artifacts 中有对应文件；
//     缺任何一个就报错，避免生成半残的 catalog
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type manifestFile struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Description   string       `json:"description"`
	Author        string       `json:"author"`
	License       string       `json:"license"`
	Homepage      string       `json:"homepage"`
	Tags          []string     `json:"tags"`
	Compatibility compatibility `json:"compatibility"`
	Platforms     []platform   `json:"platforms"`
}

type compatibility struct {
	Core     string `json:"core,omitempty"`
	SDK      string `json:"sdk,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type catalogFile struct {
	SchemaVersion int      `json:"catalogVersion"`
	Source        string   `json:"source,omitempty"`
	URL           string   `json:"url,omitempty"`
	Entries       []entry  `json:"entries"`
}

type entry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	Description string    `json:"description,omitempty"`
	Author      string    `json:"author,omitempty"`
	License     string    `json:"license,omitempty"`
	Homepage    string    `json:"homepage,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Versions    []release `json:"versions"`
}

type release struct {
	Version       string        `json:"version"`
	Compatibility compatibility `json:"compatibility"`
	Platforms     []artifact    `json:"platforms"`
	PublishedAt   string        `json:"publishedAt,omitempty"`
}

type artifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	URL      string `json:"url"`
	Checksum string `json:"checksum"`
	Size     int64  `json:"size,omitempty"`
}

func main() {
	pluginsList := flag.String("plugins", "ssh,docker,ai,pve", "comma-separated plugin ids")
	pluginsDir := flag.String("plugins-dir", "plugins", "directory containing plugins/<id>/plugin.json")
	artifactsDir := flag.String("artifacts", "artifacts", "directory containing release tar.gz files")
	baseURL := flag.String("base-url", "", "artifact URL prefix, e.g. https://github.com/mow/mow/releases/download/v0.5.1")
	version := flag.String("version", "", "release version tag, e.g. v0.5.1")
	source := flag.String("source", "official", "catalog source label")
	publishedAt := flag.String("published-at", "", "optional ISO 8601 publishedAt")
	out := flag.String("out", "catalog.json", "output path")
	flag.Parse()

	if *baseURL == "" || *version == "" {
		fatal("-base-url and -version are required")
	}
	// tarball 内命名不带 "v" 前缀
	trimmedVersion := strings.TrimPrefix(*version, "v")

	ids := strings.Split(*pluginsList, ",")
	entries := make([]entry, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		mfPath := filepath.Join(*pluginsDir, id, "plugin.json")
		mf, err := loadManifest(mfPath)
		if err != nil {
			fatal("load %s: %v", mfPath, err)
		}
		if len(mf.Platforms) == 0 {
			fatal("%s has no platforms[]", mfPath)
		}
		arts := make([]artifact, 0, len(mf.Platforms))
		for _, p := range mf.Platforms {
			// release.yml 目标命名：linux → target=linux; windows → target=windows; darwin → target=darwin
			fname := fmt.Sprintf("mow-%s-plugin-%s-%s.tar.gz", id, p.OS, p.Arch)
			full := filepath.Join(*artifactsDir, fname)
			info, err := os.Stat(full)
			if err != nil {
				fatal("missing artifact %s: %v", full, err)
			}
			sum, err := sha256File(full)
			if err != nil {
				fatal("hash %s: %v", full, err)
			}
			arts = append(arts, artifact{
				OS:       p.OS,
				Arch:     p.Arch,
				URL:      strings.TrimRight(*baseURL, "/") + "/" + fname,
				Checksum: "sha256:" + sum,
				Size:     info.Size(),
			})
		}
		entries = append(entries, entry{
			ID:          mf.ID,
			Name:        mf.Name,
			Description: mf.Description,
			Author:      mf.Author,
			License:     mf.License,
			Homepage:    mf.Homepage,
			Tags:        mf.Tags,
			Versions: []release{{
				Version:       trimmedVersion,
				Compatibility: mf.Compatibility,
				Platforms:     arts,
				PublishedAt:   *publishedAt,
			}},
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })

	doc := catalogFile{
		SchemaVersion: 1,
		Source:        *source,
		Entries:       entries,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fatal("encode: %v", err)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fatal("write %s: %v", *out, err)
	}
	fmt.Printf("wrote %s with %d entries (version=%s, base=%s)\n", *out, len(entries), trimmedVersion, *baseURL)
}

func loadManifest(path string) (manifestFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifestFile{}, err
	}
	var mf manifestFile
	if err := json.Unmarshal(raw, &mf); err != nil {
		return manifestFile{}, err
	}
	if mf.ID == "" {
		return manifestFile{}, fmt.Errorf("missing id in %s", path)
	}
	return mf, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "build-catalog: "+format+"\n", args...)
	os.Exit(1)
}
