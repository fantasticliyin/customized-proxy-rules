// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/artifact"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
)

func TestExitPaths(t *testing.T) {
	tests := []struct {
		args     []string
		code     int
		contains string
	}{{nil, 2, "usage"}, {[]string{"unknown"}, 2, "unknown command"}, {[]string{"help"}, 0, "usage"}, {[]string{"validate"}, 2, "validate requires"}}
	for _, test := range tests {
		var out, err bytes.Buffer
		code := run(test.args, &out, &err)
		if code != test.code {
			t.Errorf("%v: code %d want %d", test.args, code, test.code)
		}
		if !strings.Contains(out.String()+err.String(), test.contains) {
			t.Errorf("%v: output %q missing %q", test.args, out.String()+err.String(), test.contains)
		}
	}
}

func TestDiffReportsPureOrderChange(t *testing.T) {
	result := makeDiff("geosite", map[string][]string{"x": {"a", "b"}}, map[string][]string{"x": {"b", "a"}})
	if len(result.Categories) != 1 || !result.Categories[0].Changed || !result.Categories[0].OrderChanged || len(result.Categories[0].Added) != 0 || len(result.Categories[0].Deleted) != 0 {
		t.Fatalf("pure order change was not represented separately: %#v", result)
	}
}

func TestFilterSiteAttributeIncludesIntegerViews(t *testing.T) {
	items := []geodat.SiteProjection{{Category: "x", Rules: []geodat.SiteEntry{{Value: "a", Attrs: []string{"@rank=2"}}, {Value: "b", Attrs: []string{"@ads"}}, {Value: "c"}}}}
	filtered := filterSiteAttribute(items, "rank")
	if len(filtered[0].Rules) != 1 || filtered[0].Rules[0].Value != "a" {
		t.Fatalf("integer attribute view filter failed: %#v", filtered)
	}
}

func TestWorkflowTriggerAndHashScopesStayAligned(t *testing.T) {
	workflowPath := filepath.Join("..", "..", "..", "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	pairs := [][2]string{
		{`- "rules/**"`, "            rules \\\n"},
		{`- "tools/rule-patcher/config.yaml"`, "            tools/rule-patcher/config.yaml \\\n"},
		{`- "tools/rule-patcher/toolchain.lock.yaml"`, "            tools/rule-patcher/toolchain.lock.yaml \\\n"},
		{`- "tools/rule-patcher/cmd/**"`, "            tools/rule-patcher/cmd \\\n"},
		{`- "tools/rule-patcher/internal/**"`, "            tools/rule-patcher/internal \\\n"},
		{`- "tools/rule-patcher/go.mod"`, "            tools/rule-patcher/go.mod \\\n"},
		{`- "tools/rule-patcher/go.sum"`, "            tools/rule-patcher/go.sum \\\n"},
		{`- ".github/workflows/release.yml"`, "            .github/workflows/release.yml)"},
	}
	for _, pair := range pairs {
		if !strings.Contains(text, pair[0]) || !strings.Contains(text, pair[1]) {
			t.Fatalf("workflow trigger/hash scope drift for %q", pair[0])
		}
	}
	if strings.Contains(text, `- "tools/rule-patcher/docs/**"`) || strings.Contains(text, "            tools/rule-patcher/docs ") {
		t.Fatal("docs unexpectedly participate in publication identity")
	}
}

func TestBuildValidateInspectAndDiff(t *testing.T) {
	root := t.TempDir()
	rules := filepath.Join(root, "rules")
	for _, dir := range []string{"geosite/new", "geosite/patch", "geoip/new", "geoip/patch"} {
		if err := os.MkdirAll(filepath.Join(rules, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	site := filepath.Join(root, "site.dat")
	ip := filepath.Join(root, "ip.dat")
	if err := geodat.WriteSite(site, &v2ray.GeoSiteList{Entry: []*v2ray.GeoSite{{CountryCode: "test", Domain: []*v2ray.Domain{{Type: v2ray.Domain_Full, Value: "example.com"}}}}}, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := geodat.WriteIP(ip, &v2ray.GeoIPList{Entry: []*v2ray.GeoIP{{CountryCode: "test", Cidr: []*v2ray.CIDR{{Ip: []byte{192, 0, 2, 0}, Prefix: 24}}}}}, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	converter := filepath.Join(root, "converter")
	sing := filepath.Join(root, "sing-box")
	mihomo := filepath.Join(root, "mihomo")
	mustWrite(t, converter, "#!/bin/sh\nout=''\nwhile [ $# -gt 0 ]; do if [ \"$1\" = '-o' ]; then out=$2; shift 2; else shift; fi; done\nmkdir -p \"$out\"\nprintf '{\"version\":2,\"rules\":[{}]}\\n' > \"$out/test.json\"\nprintf old > \"$out/test.srs\"\n", 0o755)
	mustWrite(t, sing, "#!/bin/sh\ncp \"$5\" \"$4\"\n", 0o755)
	mustWrite(t, mihomo, "#!/bin/sh\nexit 0\n", 0o755)
	configPath := filepath.Join(root, "config.yaml")
	lockPath := filepath.Join(root, "lock.yaml")
	mustWrite(t, configPath, fmt.Sprintf("schema_version: 1\nupstream:\n  repository: MetaCubeX/meta-rules-dat\n  assets:\n    geosite: geosite.dat\n    geosite_checksum: geosite.dat.sha256sum\n    geoip: geoip.dat\n    geoip_checksum: geoip.dat.sha256sum\nrules_dir: %s\ndist_dir: %s\ncache_dir: %s\nmax_dat_bytes: 134217728\nworkers: 2\n", rules, filepath.Join(root, "dist"), filepath.Join(root, "cache")), 0o644)
	mustWrite(t, lockPath, "schema_version: 1\nsing_box:\n  version: v1\n  url: https://github.com/SagerNet/sing-box/releases/download/v1/sing.tar.gz\n  sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nmihomo:\n  version: v1\n  url: https://github.com/MetaCubeX/mihomo/releases/download/v1/mihomo.gz\n  sha256: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\nconverter:\n  module: github.com/metacubex/meta-rules-converter\n  version: v0.0.0-20260101000000-cccccccccccc\n  commit: cccccccccccccccccccccccccccccccccccccccc\n", 0o644)
	args := []string{"build", "--config", configPath, "--toolchain-lock", lockPath, "--geosite", site, "--geoip", ip, "--converter", converter, "--sing-box", sing, "--mihomo", mihomo, "--commit", "123456789abcdef", "--generated-at", "2026-01-01T00:00:00Z"}
	var out, stderr bytes.Buffer
	if code := run(args, &out, &stderr); code != 0 {
		t.Fatalf("build code %d: %s", code, stderr.String())
	}
	first, err := artifact.HashTree(filepath.Join(root, "dist"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "dist")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := run(args, &out, &stderr); code != 0 {
		t.Fatalf("second build code %d: %s", code, stderr.String())
	}
	second, err := artifact.HashTree(filepath.Join(root, "dist"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("build is not deterministic: %s != %s", first, second)
	}
	for _, command := range [][]string{{"validate", "--sing-box", sing, "--mihomo", mihomo, filepath.Join(root, "dist")}, {"inspect", "--dataset", "geosite", filepath.Join(root, "dist", "geosite.dat")}, {"diff", "--dataset", "geoip", "--format", "json", ip, filepath.Join(root, "dist", "geoip.dat")}} {
		out.Reset()
		stderr.Reset()
		if code := run(command, &out, &stderr); code != 0 {
			t.Fatalf("%v code %d: %s", command, code, stderr.String())
		}
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
