// SPDX-License-Identifier: GPL-3.0-only
package metadb

import (
	"bytes"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
	"github.com/oschwald/maxminddb-golang"
)

func TestGenerateIsDeterministicAndPreservesOverlappingCategories(t *testing.T) {
	dir := t.TempDir()
	dat := filepath.Join(dir, "geoip.dat")
	list := fixtureList()
	if err := geodat.WriteIP(dat, list, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	generatedAt := time.Date(2026, 9, 3, 1, 2, 3, 456, time.UTC)
	one := filepath.Join(dir, "one.metadb")
	two := filepath.Join(dir, "two.metadb")
	if err := Generate(dat, one, generatedAt, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := Generate(dat, two, generatedAt, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	oneData, err := os.ReadFile(one)
	if err != nil {
		t.Fatal(err)
	}
	twoData, err := os.ReadFile(two)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oneData, twoData) {
		t.Fatal("identical input and generated_at produced different MetaDB bytes")
	}
	reader, err := maxminddb.Open(one)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.Metadata.DatabaseType != DatabaseType || reader.Metadata.IPVersion != IPVersion || reader.Metadata.RecordSize != RecordSize || reader.Metadata.BuildEpoch != uint(generatedAt.Unix()) {
		t.Fatalf("unexpected metadata: %#v", reader.Metadata)
	}
	assertCodes(t, one, "100.64.0.1", "private", "tailscale")
	assertCodes(t, one, "fd7a:115c:a1e0::1", "private6", "tailscale")
}

func TestGenerateRejectsInvalidInputAndTimestamp(t *testing.T) {
	dir := t.TempDir()
	invalid := filepath.Join(dir, "invalid.dat")
	if err := os.WriteFile(invalid, []byte("not protobuf"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Generate(invalid, filepath.Join(dir, "invalid.metadb"), time.Unix(1234, 0), geodat.DefaultMaxBytes); err == nil {
		t.Fatal("invalid DAT accepted")
	}
	empty := filepath.Join(dir, "empty.dat")
	if err := geodat.WriteIP(empty, &v2ray.GeoIPList{}, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := Generate(empty, filepath.Join(dir, "empty.metadb"), time.Unix(1234, 0), geodat.DefaultMaxBytes); err == nil {
		t.Fatal("empty DAT accepted")
	}
	valid := filepath.Join(dir, "valid.dat")
	if err := geodat.WriteIP(valid, fixtureList(), geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := Generate(valid, filepath.Join(dir, "epoch.metadb"), time.Unix(0, 0), geodat.DefaultMaxBytes); err == nil {
		t.Fatal("unsupported zero build epoch accepted")
	}
}

func TestGenerateRejectsGeoIPSemanticsMetaDBCannotRepresent(t *testing.T) {
	for name, category := range map[string]*v2ray.GeoIP{
		"reverse-match": {CountryCode: "reverse", ReverseMatch: true, Cidr: []*v2ray.CIDR{{Ip: []byte{192, 0, 2, 0}, Prefix: 24}}},
		"empty":         {CountryCode: "empty"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dat := filepath.Join(dir, "geoip.dat")
			if err := geodat.WriteIP(dat, &v2ray.GeoIPList{Entry: []*v2ray.GeoIP{category}}, geodat.DefaultMaxBytes); err != nil {
				t.Fatal(err)
			}
			if err := Generate(dat, filepath.Join(dir, "geoip.metadb"), time.Unix(1234, 0), geodat.DefaultMaxBytes); err == nil {
				t.Fatalf("%s GeoIP category was silently converted", name)
			}
		})
	}
}

func TestValidateRejectsMetadataMismatch(t *testing.T) {
	dir := t.TempDir()
	dat := filepath.Join(dir, "geoip.dat")
	list := fixtureList()
	if err := geodat.WriteIP(dat, list, geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "geoip.metadb")
	if err := Generate(dat, path, time.Unix(1234, 0), geodat.DefaultMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, list, time.Unix(1235, 0), geodat.DefaultMaxBytes); err == nil {
		t.Fatal("incorrect build_epoch accepted")
	}
}

func fixtureList() *v2ray.GeoIPList {
	return &v2ray.GeoIPList{Entry: []*v2ray.GeoIP{
		{CountryCode: "private", Cidr: []*v2ray.CIDR{{Ip: []byte{100, 0, 0, 0}, Prefix: 8}}},
		{CountryCode: "private6", Cidr: []*v2ray.CIDR{{Ip: netip.MustParseAddr("fd00::").AsSlice(), Prefix: 8}}},
		{CountryCode: "tailscale", Cidr: []*v2ray.CIDR{
			{Ip: []byte{100, 64, 0, 0}, Prefix: 10},
			{Ip: netip.MustParseAddr("fd7a:115c:a1e0::").AsSlice(), Prefix: 48},
		}},
	}}
}

func assertCodes(t *testing.T, path, address string, want ...string) {
	t.Helper()
	codes, err := LookupCodes(path, netip.MustParseAddr(address))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range want {
		if !contains(codes, expected) {
			t.Fatalf("lookup %s returned %v, missing %s", address, codes, expected)
		}
	}
}
