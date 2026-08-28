// SPDX-License-Identifier: GPL-3.0-only
package validate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/artifact"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/srs"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/upstream"
)

type Report struct {
	SchemaVersion     int    `json:"schema_version"`
	Version           string `json:"version"`
	Files             int    `json:"files"`
	GeoSiteCategories int    `json:"geosite_categories"`
	GeoIPCategories   int    `json:"geoip_categories"`
	Valid             bool   `json:"valid"`
}

var ErrExternalTool = errors.New("external validation tool failed")

func DistWithTools(ctx context.Context, dist string, maxBytes int64, tools srs.Tools, mihomo string) (Report, error) {
	report, err := Dist(dist, maxBytes)
	if err != nil {
		return report, err
	}
	site, err := geodat.LoadSite(filepath.Join(dist, "geosite.dat"), maxBytes)
	if err != nil {
		return report, err
	}
	if len(site.Entry) == 0 {
		return report, fmt.Errorf("geosite DAT has no representative category")
	}
	siteRepresentative := strings.ToLower(site.Entry[0].CountryCode)
	site = nil
	ip, err := geodat.LoadIP(filepath.Join(dist, "geoip.dat"), maxBytes)
	if err != nil {
		return report, err
	}
	if len(ip.Entry) == 0 {
		return report, fmt.Errorf("geoip DAT has no representative category")
	}
	ipRepresentative := strings.ToLower(ip.Entry[0].CountryCode)
	paths, err := srsIndex(dist)
	if err != nil {
		return report, err
	}
	if err := srs.ValidateCompiled(ctx, tools.SingBox, dist, paths); err != nil {
		return report, fmt.Errorf("%w: %v", ErrExternalTool, err)
	}
	if err := srs.MihomoSmoke(ctx, mihomo, filepath.Join(dist, "geosite.dat"), filepath.Join(dist, "geoip.dat"), siteRepresentative, ipRepresentative); err != nil {
		return report, fmt.Errorf("%w: %v", ErrExternalTool, err)
	}
	return report, nil
}

func Dist(dist string, maxBytes int64) (Report, error) {
	var report Report
	manifest, err := artifact.ReadManifest(filepath.Join(dist, "manifest.json"))
	if err != nil {
		return report, fmt.Errorf("manifest: %w", err)
	}
	report.SchemaVersion = 1
	report.Version = manifest.Version
	if manifest.Version == "" || manifest.CustomCommit == "" || len(manifest.ReleaseInputsHash) != 64 || manifest.GeneratedAt.IsZero() || manifest.License != "GPL-3.0-only" || manifest.Upstream.Repository != "MetaCubeX/meta-rules-dat" {
		return report, fmt.Errorf("manifest provenance is incomplete")
	}
	if _, err := hex.DecodeString(manifest.ReleaseInputsHash); err != nil {
		return report, fmt.Errorf("manifest release_inputs_hash is not hexadecimal")
	}
	if manifest.Tools.SingBox == "" || manifest.Tools.Mihomo == "" || manifest.Tools.ConverterModule != "github.com/metacubex/meta-rules-converter" || manifest.Tools.ConverterVersion == "" || len(manifest.Tools.ConverterCommit) != 40 {
		return report, fmt.Errorf("manifest toolchain provenance is incomplete")
	}
	if manifest.Upstream.ReleaseID > 0 {
		wantVersion := fmt.Sprintf("u%d-c%s", manifest.Upstream.ReleaseID, manifest.CustomCommit[:min(12, len(manifest.CustomCommit))])
		if len(manifest.CustomCommit) != 40 || manifest.Version != wantVersion || manifest.Upstream.PublishedAt.IsZero() || !validEvidence(manifest.Upstream.GeoSiteAsset) || !validEvidence(manifest.Upstream.GeoIPAsset) || !validEvidence(manifest.Upstream.GeoSiteChecksum) || !validEvidence(manifest.Upstream.GeoIPChecksum) {
			return report, fmt.Errorf("manifest immutable upstream identity is incomplete")
		}
		if strings.TrimPrefix(manifest.Upstream.GeoSiteAsset.Digest, "sha256:") != manifest.Upstream.GeoSiteSHA256 || strings.TrimPrefix(manifest.Upstream.GeoIPAsset.Digest, "sha256:") != manifest.Upstream.GeoIPSHA256 {
			return report, fmt.Errorf("manifest upstream DAT digests disagree")
		}
	}
	checks, err := readChecksums(filepath.Join(dist, "SHA256SUMS"))
	if err != nil {
		return report, err
	}
	rootFiles := []string{"geosite.dat", "geoip.dat", "manifest.json", "srs.tar.gz"}
	if len(checks) != len(rootFiles) {
		return report, fmt.Errorf("SHA256SUMS must contain exactly the four Release assets")
	}
	for _, name := range rootFiles {
		want, ok := checks[name]
		if !ok {
			return report, fmt.Errorf("SHA256SUMS missing %s", name)
		}
		got, err := hash(filepath.Join(dist, name))
		if err != nil {
			return report, err
		}
		if got != want {
			return report, fmt.Errorf("checksum mismatch for %s", name)
		}
		report.Files++
	}
	site, err := geodat.LoadSite(filepath.Join(dist, "geosite.dat"), maxBytes)
	if err != nil {
		return report, err
	}
	ip, err := geodat.LoadIP(filepath.Join(dist, "geoip.dat"), maxBytes)
	if err != nil {
		return report, err
	}
	report.GeoSiteCategories = len(site.Entry)
	report.GeoIPCategories = len(ip.Entry)
	siteNames, err := srs.ExpectedSite(site)
	if err != nil {
		return report, err
	}
	ipNames, err := srs.ExpectedIP(ip)
	if err != nil {
		return report, err
	}
	expectedSRS := make([]string, 0, len(siteNames)+len(ipNames))
	for _, name := range siteNames {
		expectedSRS = append(expectedSRS, "geosite/"+name+".srs")
	}
	for _, name := range ipNames {
		expectedSRS = append(expectedSRS, "geoip/"+name+".srs")
	}
	sort.Strings(expectedSRS)
	disk, err := srsIndex(dist)
	if err != nil {
		return report, err
	}
	if !equal(expectedSRS, disk) {
		return report, fmt.Errorf("DAT/SRS index mismatch: expected %v, got %v", expectedSRS, disk)
	}
	archive, err := archiveContents(filepath.Join(dist, "srs.tar.gz"), manifest.GeneratedAt)
	if err != nil {
		return report, err
	}
	if len(archive) != len(disk) {
		return report, fmt.Errorf("SRS tar index differs from dist index")
	}
	for _, name := range disk {
		got, err := hash(filepath.Join(dist, filepath.FromSlash(name)))
		if err != nil {
			return report, err
		}
		if archive[name] != got {
			return report, fmt.Errorf("SRS tar content differs from dist for %s", name)
		}
	}
	if manifest.Categories.GeoSite != len(site.Entry) || manifest.Categories.GeoIP != len(ip.Entry) {
		return report, fmt.Errorf("manifest category counts differ from DAT")
	}
	if len(disk) != manifest.Categories.GeoSite+manifest.Categories.GeoSiteViews+manifest.Categories.GeoIP {
		return report, fmt.Errorf("manifest SRS count differs from disk")
	}
	expectedManifestFiles := append([]string{"geosite.dat", "geoip.dat", "srs.tar.gz"}, disk...)
	sort.Strings(expectedManifestFiles)
	if len(manifest.Files) != len(expectedManifestFiles) {
		return report, fmt.Errorf("manifest file inventory count mismatch")
	}
	for _, name := range expectedManifestFiles {
		recorded, ok := manifest.Files[name]
		if !ok || !safeRelative(name) || len(recorded.SHA256) != 64 || recorded.Size <= 0 {
			return report, fmt.Errorf("manifest has invalid or missing file metadata for %s", name)
		}
		got, err := hash(filepath.Join(dist, filepath.FromSlash(name)))
		if err != nil {
			return report, err
		}
		info, err := os.Stat(filepath.Join(dist, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() || got != strings.ToLower(recorded.SHA256) || info.Size() != recorded.Size {
			return report, fmt.Errorf("manifest file metadata mismatch for %s", name)
		}
	}
	branch := filepath.Join(dist, "release")
	branchChecks, err := readChecksums(filepath.Join(branch, "SHA256SUMS"))
	if err != nil {
		return report, fmt.Errorf("release snapshot checksums: %w", err)
	}
	expectedBranch := append([]string{"manifest.json"}, disk...)
	sort.Strings(expectedBranch)
	if len(branchChecks) != len(expectedBranch) {
		return report, fmt.Errorf("release snapshot file count mismatch")
	}
	for _, name := range expectedBranch {
		want, ok := branchChecks[name]
		if !ok {
			return report, fmt.Errorf("release snapshot missing %s", name)
		}
		got, err := hash(filepath.Join(branch, filepath.FromSlash(name)))
		if err != nil {
			return report, err
		}
		if got != want {
			return report, fmt.Errorf("release snapshot checksum mismatch for %s", name)
		}
	}
	actualBranch, err := snapshotIndex(branch)
	if err != nil {
		return report, err
	}
	actualBranch = removeString(actualBranch, "SHA256SUMS")
	if !equal(actualBranch, expectedBranch) {
		return report, fmt.Errorf("release snapshot contains unexpected files")
	}
	rootManifest, err := os.ReadFile(filepath.Join(dist, "manifest.json"))
	if err != nil {
		return report, err
	}
	branchManifest, err := os.ReadFile(filepath.Join(branch, "manifest.json"))
	if err != nil {
		return report, err
	}
	if !bytes.Equal(rootManifest, branchManifest) {
		return report, fmt.Errorf("release snapshot manifest differs from dist")
	}
	report.Files += len(disk)
	report.Valid = true
	return report, nil
}
func readChecksums(path string) (map[string]string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 16<<20 {
		return nil, fmt.Errorf("checksum file must be a non-empty regular file no larger than 16 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for lineNo, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return nil, fmt.Errorf("invalid checksum line %d", lineNo+1)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return nil, fmt.Errorf("invalid checksum digest on line %d", lineNo+1)
		}
		if _, exists := out[fields[1]]; exists {
			return nil, fmt.Errorf("duplicate checksum path %s", fields[1])
		}
		if !safeRelative(fields[1]) {
			return nil, fmt.Errorf("unsafe checksum path %s", fields[1])
		}
		out[fields[1]] = strings.ToLower(fields[0])
	}
	return out, nil
}
func hash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact is not a regular file: %s", path)
	}
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
func archiveContents(path string, generatedAt time.Time) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	contents := map[string]string{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg || header.Uid != 0 || header.Gid != 0 || header.Mode != 0o644 || !safeRelative(header.Name) || filepath.Ext(header.Name) != ".srs" || header.Size <= 0 || header.Size > 64<<20 {
			return nil, fmt.Errorf("unsafe tar entry %q", header.Name)
		}
		if !header.ModTime.UTC().Equal(generatedAt.UTC().Truncate(time.Second)) {
			return nil, fmt.Errorf("tar entry %q has non-deterministic timestamp", header.Name)
		}
		if _, exists := contents[header.Name]; exists {
			return nil, fmt.Errorf("duplicate tar entry %q", header.Name)
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(tr, header.Size+1))
		if err != nil || n != header.Size {
			return nil, fmt.Errorf("invalid tar content %q", header.Name)
		}
		contents[header.Name] = hex.EncodeToString(h.Sum(nil))
	}
	return contents, nil
}
func srsIndex(dist string) ([]string, error) {
	var names []string
	for _, dataset := range []string{"geosite", "geoip"} {
		entries, err := os.ReadDir(filepath.Join(dist, dataset))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".srs" {
				return nil, fmt.Errorf("unexpected SRS entry %s/%s", dataset, entry.Name())
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				return nil, fmt.Errorf("invalid SRS %s/%s", dataset, entry.Name())
			}
			names = append(names, dataset+"/"+entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func safeRelative(name string) bool {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") || strings.ContainsRune(name, '\x00') {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	return clean == name && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func snapshotIndex(root string) ([]string, error) {
	var names []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release snapshot contains symlink %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(names)
	return names, err
}

func removeString(values []string, target string) []string {
	out := values[:0]
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}

func validEvidence(value upstream.AssetEvidence) bool {
	if value.ID <= 0 || value.Name == "" || value.Size <= 0 || len(value.Digest) != len("sha256:")+64 || !strings.HasPrefix(value.Digest, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value.Digest, "sha256:"))
	return err == nil
}
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
