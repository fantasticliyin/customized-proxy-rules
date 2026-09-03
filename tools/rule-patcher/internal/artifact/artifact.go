// SPDX-License-Identifier: GPL-3.0-only
package artifact

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/overlay"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/upstream"
)

const (
	LegacyManifestSchema = 1
	ManifestSchema       = 2
)

type ToolVersions struct {
	SingBox          string `json:"sing_box"`
	Mihomo           string `json:"mihomo"`
	ConverterModule  string `json:"converter_module"`
	ConverterVersion string `json:"converter_version"`
	ConverterCommit  string `json:"converter_commit"`
}
type FileInfo struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type CategoryCount struct {
	GeoSite      int `json:"geosite"`
	GeoSiteViews int `json:"geosite_views"`
	GeoIP        int `json:"geoip"`
}
type Manifest struct {
	SchemaVersion     int                     `json:"schema_version"`
	Version           string                  `json:"version"`
	GeneratedAt       time.Time               `json:"generated_at"`
	CustomCommit      string                  `json:"custom_commit"`
	ReleaseInputsHash string                  `json:"release_inputs_hash"`
	Upstream          upstream.Provenance     `json:"upstream"`
	Tools             ToolVersions            `json:"tools"`
	Categories        CategoryCount           `json:"categories"`
	PatchStats        []overlay.CategoryStats `json:"patch_stats"`
	Warnings          []overlay.Warning       `json:"warnings"`
	Files             map[string]FileInfo     `json:"files"`
	License           string                  `json:"license"`
	Attribution       string                  `json:"attribution"`
}

func Finalize(dist string, manifest Manifest) error {
	manifest.SchemaVersion = ManifestSchema
	manifest.License = "GPL-3.0-only"
	manifest.Attribution = "Derived from MetaCubeX/meta-rules-dat (GPL-3.0)"
	epoch := manifest.GeneratedAt.UTC().Truncate(time.Second)
	if epoch.IsZero() {
		return fmt.Errorf("manifest generated_at is required")
	}
	if err := createTar(filepath.Join(dist, "srs.tar.gz"), dist, epoch); err != nil {
		return err
	}
	manifest.Files = map[string]FileInfo{}
	files := []string{"geosite.dat", "geoip.dat", "geoip.metadb", "srs.tar.gz"}
	for _, dataset := range []string{"geosite", "geoip"} {
		entries, err := os.ReadDir(filepath.Join(dist, dataset))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".srs" {
				return fmt.Errorf("unexpected SRS artifact %s/%s", dataset, entry.Name())
			}
			files = append(files, dataset+"/"+entry.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		info, err := hashFile(filepath.Join(dist, name))
		if err != nil {
			return err
		}
		manifest.Files[name] = info
	}
	data, err := marshalJSON(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dist, "manifest.json"), data, 0o644); err != nil {
		return err
	}
	if err := writeChecksums(filepath.Join(dist, "SHA256SUMS"), dist, []string{"geosite.dat", "geoip.dat", "geoip.metadb", "manifest.json", "srs.tar.gz"}); err != nil {
		return err
	}
	return writeBranchSnapshot(dist)
}

func ReadManifest(path string) (Manifest, error) {
	var m Manifest
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 16<<20 {
		return m, fmt.Errorf("manifest must be a non-empty regular file no larger than 16 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil {
		return m, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return m, fmt.Errorf("manifest contains trailing data")
	}
	if m.SchemaVersion != LegacyManifestSchema && m.SchemaVersion != ManifestSchema {
		return m, fmt.Errorf("manifest schema must be %d or %d", LegacyManifestSchema, ManifestSchema)
	}
	return m, nil
}

func HashTree(root string) (string, error) {
	h := sha256.New()
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("hash tree contains symlink: %s", path)
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("hash tree contains non-regular file: %s", path)
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	for _, rel := range paths {
		full := filepath.Join(root, filepath.FromSlash(rel))
		info, err := hashFile(full)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%s\x00%d\n", rel, info.SHA256, info.Size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func HashFiles(root string, paths []string) (string, error) {
	h := sha256.New()
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	for _, rel := range sorted {
		info, err := hashFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%s\x00%d\n", filepath.ToSlash(rel), info.SHA256, info.Size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func createTar(target, dist string, epoch time.Time) error {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	gz.Header.ModTime = epoch
	gz.Header.Name = ""
	tw := tar.NewWriter(gz)
	var files []string
	for _, dataset := range []string{"geosite", "geoip"} {
		root := filepath.Join(dist, dataset)
		entries, err := os.ReadDir(root)
		if err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".srs" {
				return fmt.Errorf("unexpected SRS entry %s/%s", dataset, entry.Name())
			}
			files = append(files, dataset+"/"+entry.Name())
		}
	}
	sort.Strings(files)
	for _, rel := range files {
		path := filepath.Join(dist, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil {
			return closeTar(tw, gz, f, err)
		}
		header := &tar.Header{Name: rel, Mode: 0o644, Size: info.Size(), ModTime: epoch, AccessTime: epoch, ChangeTime: epoch, Uid: 0, Gid: 0, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			return closeTar(tw, gz, f, err)
		}
		src, err := os.Open(path)
		if err != nil {
			return closeTar(tw, gz, f, err)
		}
		_, copyErr := io.Copy(tw, src)
		closeErr := src.Close()
		if copyErr != nil {
			return closeTar(tw, gz, f, copyErr)
		}
		if closeErr != nil {
			return closeTar(tw, gz, f, closeErr)
		}
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		f.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
func closeTar(tw *tar.Writer, gz *gzip.Writer, f *os.File, err error) error {
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()
	return err
}
func writeBranchSnapshot(dist string) error {
	branch := filepath.Join(dist, "release")
	if err := os.RemoveAll(branch); err != nil {
		return err
	}
	if err := os.MkdirAll(branch, 0o755); err != nil {
		return err
	}
	for _, dataset := range []string{"geosite", "geoip"} {
		if err := copyDir(filepath.Join(dist, dataset), filepath.Join(branch, dataset)); err != nil {
			return err
		}
	}
	for _, name := range []string{"manifest.json"} {
		if err := copyFile(filepath.Join(dist, name), filepath.Join(branch, name)); err != nil {
			return err
		}
	}
	files, err := branchFiles(branch)
	if err != nil {
		return err
	}
	return writeChecksums(filepath.Join(branch, "SHA256SUMS"), branch, files)
}
func branchFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release snapshot contains symlink: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if filepath.ToSlash(rel) != "SHA256SUMS" {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || infoErr != nil || !info.Mode().IsRegular() || filepath.Ext(entry.Name()) != ".srs" {
			return fmt.Errorf("unexpected directory in SRS output: %s", entry.Name())
		}
		if err := copyFile(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
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
func writeChecksums(target, root string, names []string) error {
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		info, err := hashFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s  %s\n", info.SHA256, filepath.ToSlash(name))
	}
	return os.WriteFile(target, []byte(b.String()), 0o644)
}
func hashFile(path string) (FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileInfo{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}
func marshalJSON(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
