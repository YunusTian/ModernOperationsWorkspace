//go:build ignore

// package-plugin creates one target-specific plugin package from a manifest
// template and a compiled binary. It prunes platforms[] to the selected target,
// injects the release version and real binary checksum, then writes the binary
// at the entrypoint declared by the manifest.
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
)

type manifestDoc struct {
	Version   string                     `json:"version"`
	Platforms []platformDoc              `json:"platforms"`
	Extra     map[string]json.RawMessage `json:"-"`
}
type platformDoc struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Entrypoint string `json:"entrypoint"`
	Checksum   string `json:"checksum"`
}

func main() {
	manifestPath := flag.String("manifest", "", "source plugin.json")
	binaryPath := flag.String("binary", "", "compiled plugin binary")
	targetOS := flag.String("os", "", "target GOOS")
	targetArch := flag.String("arch", "", "target GOARCH")
	version := flag.String("version", "", "release version")
	outDir := flag.String("out", "", "output package directory")
	flag.Parse()
	if *manifestPath == "" || *binaryPath == "" || *targetOS == "" || *targetArch == "" || *version == "" || *outDir == "" {
		fatal("all flags are required")
	}

	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		fatal("read manifest: %v", err)
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal(raw, &all); err != nil {
		fatal("decode manifest: %v", err)
	}
	var platforms []platformDoc
	if err = json.Unmarshal(all["platforms"], &platforms); err != nil {
		fatal("decode platforms: %v", err)
	}
	var selected *platformDoc
	for i := range platforms {
		if platforms[i].OS == *targetOS && platforms[i].Arch == *targetArch {
			p := platforms[i]
			selected = &p
			break
		}
	}
	if selected == nil {
		fatal("manifest has no platform %s/%s", *targetOS, *targetArch)
	}

	sum, err := hashFile(*binaryPath)
	if err != nil {
		fatal("hash binary: %v", err)
	}
	selected.Checksum = "sha256:" + sum
	versionJSON, _ := json.Marshal(*version)
	platformJSON, _ := json.Marshal([]platformDoc{*selected})
	all["version"] = versionJSON
	all["platforms"] = platformJSON

	if err = os.RemoveAll(*outDir); err != nil {
		fatal("clean output: %v", err)
	}
	destination := filepath.Join(*outDir, filepath.FromSlash(selected.Entrypoint))
	if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		fatal("mkdir output: %v", err)
	}
	if err = copyFile(*binaryPath, destination); err != nil {
		fatal("copy binary: %v", err)
	}
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		fatal("encode manifest: %v", err)
	}
	if err = os.WriteFile(filepath.Join(*outDir, "plugin.json"), append(out, '\n'), 0o644); err != nil {
		fatal("write manifest: %v", err)
	}
	fmt.Printf("packaged %s/%s checksum=sha256:%s\n", *targetOS, *targetArch, sum)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "package-plugin: "+format+"\n", args...)
	os.Exit(1)
}
