// SPDX-License-Identifier: GPL-3.0-only
package config

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = 1
const upstreamRepository = "MetaCubeX/meta-rules-dat"

type AssetNames struct {
	GeoSite         string `yaml:"geosite"`
	GeoSiteChecksum string `yaml:"geosite_checksum"`
	GeoIP           string `yaml:"geoip"`
	GeoIPChecksum   string `yaml:"geoip_checksum"`
}

type Upstream struct {
	Repository string     `yaml:"repository"`
	Assets     AssetNames `yaml:"assets"`
}

type Config struct {
	SchemaVersion int      `yaml:"schema_version"`
	Upstream      Upstream `yaml:"upstream"`
	RulesDir      string   `yaml:"rules_dir"`
	DistDir       string   `yaml:"dist_dir"`
	CacheDir      string   `yaml:"cache_dir"`
	MaxDATBytes   int64    `yaml:"max_dat_bytes"`
	Workers       int      `yaml:"workers"`
}

type Binary struct {
	Version string `yaml:"version"`
	URL     string `yaml:"url"`
	SHA256  string `yaml:"sha256"`
}

type Converter struct {
	Module  string `yaml:"module"`
	Version string `yaml:"version"`
	Commit  string `yaml:"commit"`
}

type Toolchain struct {
	SchemaVersion int       `yaml:"schema_version"`
	SingBox       Binary    `yaml:"sing_box"`
	Mihomo        Binary    `yaml:"mihomo"`
	Converter     Converter `yaml:"converter"`
}

func Load(path string) (Config, error) {
	var cfg Config
	if err := decodeStrict(path, &cfg); err != nil {
		return cfg, err
	}
	if cfg.SchemaVersion != SchemaVersion {
		return cfg, fmt.Errorf("config schema_version must be %d, got %d", SchemaVersion, cfg.SchemaVersion)
	}
	if cfg.Upstream.Repository != upstreamRepository {
		return cfg, fmt.Errorf("upstream repository must be %s", upstreamRepository)
	}
	wantAssets := AssetNames{GeoSite: "geosite.dat", GeoSiteChecksum: "geosite.dat.sha256sum", GeoIP: "geoip.dat", GeoIPChecksum: "geoip.dat.sha256sum"}
	if cfg.Upstream.Assets != wantAssets {
		return cfg, fmt.Errorf("upstream assets must be geosite.dat/geosite.dat.sha256sum and geoip.dat/geoip.dat.sha256sum")
	}
	if cfg.MaxDATBytes <= 0 || cfg.MaxDATBytes > 128<<20 {
		return cfg, fmt.Errorf("max_dat_bytes must be within 1..134217728")
	}
	if cfg.Workers < 1 || cfg.Workers > 16 {
		return cfg, fmt.Errorf("workers must be within 1..16")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return cfg, fmt.Errorf("resolve config directory: %w", err)
	}
	cfg.RulesDir = resolve(base, cfg.RulesDir)
	cfg.DistDir = resolve(base, cfg.DistDir)
	cfg.CacheDir = resolve(base, cfg.CacheDir)
	return cfg, nil
}

func LoadToolchain(path string) (Toolchain, error) {
	var lock Toolchain
	if err := decodeStrict(path, &lock); err != nil {
		return lock, err
	}
	if lock.SchemaVersion != SchemaVersion {
		return lock, fmt.Errorf("toolchain schema_version must be %d, got %d", SchemaVersion, lock.SchemaVersion)
	}
	if lock.SingBox.Version == "" || lock.SingBox.URL == "" || len(lock.SingBox.SHA256) != 64 || lock.Mihomo.Version == "" || lock.Mihomo.URL == "" || len(lock.Mihomo.SHA256) != 64 {
		return lock, fmt.Errorf("toolchain binaries require version, URL, and 64-character sha256")
	}
	if _, err := hex.DecodeString(lock.SingBox.SHA256); err != nil {
		return lock, fmt.Errorf("sing_box sha256 must be hexadecimal")
	}
	if _, err := hex.DecodeString(lock.Mihomo.SHA256); err != nil {
		return lock, fmt.Errorf("mihomo sha256 must be hexadecimal")
	}
	binaries := []struct {
		name, raw, version, prefix string
	}{{"sing_box", lock.SingBox.URL, lock.SingBox.Version, "/SagerNet/sing-box/releases/download/"}, {"mihomo", lock.Mihomo.URL, lock.Mihomo.Version, "/MetaCubeX/mihomo/releases/download/"}}
	for _, binary := range binaries {
		name, raw := binary.name, binary.raw
		parsed, err := url.Parse(raw)
		wantPath := binary.prefix + url.PathEscape(binary.version) + "/"
		if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasPrefix(parsed.EscapedPath(), wantPath) {
			return lock, fmt.Errorf("%s URL must be an immutable github.com HTTPS release asset", name)
		}
	}
	if lock.Converter.Module == "" || lock.Converter.Version == "" || len(lock.Converter.Commit) != 40 {
		return lock, fmt.Errorf("converter requires module, immutable version, and 40-character commit")
	}
	if lock.Converter.Module != "github.com/metacubex/meta-rules-converter" {
		return lock, fmt.Errorf("converter module must be github.com/metacubex/meta-rules-converter")
	}
	if !strings.HasSuffix(lock.Converter.Version, "-"+lock.Converter.Commit[:12]) {
		return lock, fmt.Errorf("converter pseudo-version does not match locked commit")
	}
	return lock, nil
}

func decodeStrict(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 1<<20 {
		return fmt.Errorf("%s must be a non-empty regular file no larger than 1 MiB", path)
	}
	dec := yaml.NewDecoder(io.LimitReader(f, info.Size()))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode %s: multiple YAML documents are not allowed", path)
		}
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func resolve(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}
