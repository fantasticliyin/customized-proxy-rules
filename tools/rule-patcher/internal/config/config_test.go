// SPDX-License-Identifier: GPL-3.0-only
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStrictAndRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "schema_version: 1\nupstream:\n  repository: MetaCubeX/meta-rules-dat\n  assets:\n    geosite: geosite.dat\n    geosite_checksum: geosite.dat.sha256sum\n    geoip: geoip.dat\n    geoip_checksum: geoip.dat.sha256sum\nrules_dir: ../rules\ndist_dir: ../dist\ncache_dir: cache\nmax_dat_bytes: 1024\nworkers: 2\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RulesDir != filepath.Clean(filepath.Join(dir, "../rules")) {
		t.Fatalf("unexpected rules path %s", cfg.RulesDir)
	}
	if err := os.WriteFile(path, []byte(content+"unknown: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown field accepted")
	}
}
