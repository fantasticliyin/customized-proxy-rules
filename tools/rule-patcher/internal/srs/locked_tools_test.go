// SPDX-License-Identifier: GPL-3.0-only
package srs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/config"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/toolchain"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
)

func TestLockedTools(t *testing.T) {
	if os.Getenv("RUN_LOCKED_TOOL_TEST") != "1" {
		t.Skip("set RUN_LOCKED_TOOL_TEST=1 for networked locked-tool integration")
	}
	lock, err := config.LoadToolchain(filepath.Join("..", "..", "toolchain.lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	paths, err := toolchain.Prepare(ctx, lock, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sitePath := filepath.Join(dir, "geosite.dat")
	ipPath := filepath.Join(dir, "geoip.dat")
	site := &v2ray.GeoSiteList{Entry: []*v2ray.GeoSite{{CountryCode: "cn", Domain: []*v2ray.Domain{{Type: v2ray.Domain_Full, Value: "cn.example"}}}, {CountryCode: "test", Domain: []*v2ray.Domain{{Type: v2ray.Domain_Full, Value: "example.com", Attribute: []*v2ray.Domain_Attribute{{Key: "ads", TypedValue: &v2ray.Domain_Attribute_BoolValue{BoolValue: true}}, {Key: "rank", TypedValue: &v2ray.Domain_Attribute_IntValue{IntValue: 2}}}}}}}}
	ip := &v2ray.GeoIPList{Entry: []*v2ray.GeoIP{{CountryCode: "cn", Cidr: []*v2ray.CIDR{{Ip: []byte{198, 51, 100, 0}, Prefix: 24}}}, {CountryCode: "test", Cidr: []*v2ray.CIDR{{Ip: []byte{192, 0, 2, 0}, Prefix: 24}, {Ip: []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, Prefix: 32}}}}}
	if err := geodat.WriteSite(sitePath, site, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := geodat.WriteIP(ipPath, ip, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	siteIndex, err := ExpectedSite(site)
	if err != nil {
		t.Fatal(err)
	}
	ipIndex, err := ExpectedIP(ip)
	if err != nil {
		t.Fatal(err)
	}
	tools := Tools{Converter: paths.Converter, SingBox: paths.SingBox, Workers: 2}
	if err := Generate(ctx, tools, "geosite", sitePath, filepath.Join(dir, "geosite"), siteIndex); err != nil {
		t.Fatal(err)
	}
	if err := Generate(ctx, tools, "geoip", ipPath, filepath.Join(dir, "geoip"), ipIndex); err != nil {
		t.Fatal(err)
	}
	if err := MihomoSmoke(ctx, paths.Mihomo, sitePath, ipPath, "test", "test"); err != nil {
		t.Fatal(err)
	}
}
