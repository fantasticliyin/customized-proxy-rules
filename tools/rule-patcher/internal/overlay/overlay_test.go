// SPDX-License-Identifier: GPL-3.0-only
package overlay

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/geodat"
	"github.com/liyin/customized-proxy-rules/tools/rule-patcher/internal/rulescsv"
	v2ray "github.com/metacubex/geo/encoding/v2raygeo"
)

func TestApplySitePreservesUnknownFields(t *testing.T) {
	domain := &v2ray.Domain{Type: v2ray.Domain_Full, Value: "example.com"}
	category := &v2ray.GeoSite{CountryCode: "x", Domain: []*v2ray.Domain{domain}}
	input := &v2ray.GeoSiteList{Entry: []*v2ray.GeoSite{category}}
	unknown := []byte{0xa0, 0x06, 0x01}
	input.ProtoReflect().SetUnknown(unknown)
	category.ProtoReflect().SetUnknown(unknown)
	domain.ProtoReflect().SetUnknown(unknown)
	records := []rulescsv.Record{{Dataset: rulescsv.GeoSite, Mode: rulescsv.Patch, Category: "x", Op: rulescsv.Add, Site: &rulescsv.SiteRule{Type: rulescsv.Domain, Value: "new.example"}}}
	out, _, err := ApplySite(input, records)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.ProtoReflect().GetUnknown(), unknown) || !bytes.Equal(out.Entry[0].ProtoReflect().GetUnknown(), unknown) || !bytes.Equal(out.Entry[0].Domain[0].ProtoReflect().GetUnknown(), unknown) {
		t.Fatal("protobuf unknown fields were not preserved")
	}
}

func TestApplySiteAttributeSemanticsAndWarnings(t *testing.T) {
	input := &v2ray.GeoSiteList{Entry: []*v2ray.GeoSite{{CountryCode: "TEST", Domain: []*v2ray.Domain{{Type: v2ray.Domain_Full, Value: "example.com", Attribute: []*v2ray.Domain_Attribute{{Key: "count", TypedValue: &v2ray.Domain_Attribute_IntValue{IntValue: 2}}}}, {Type: v2ray.Domain_Full, Value: "example.com", Attribute: []*v2ray.Domain_Attribute{{Key: "ads", TypedValue: &v2ray.Domain_Attribute_BoolValue{BoolValue: true}}}}}}}}
	records := []rulescsv.Record{{Dataset: rulescsv.GeoSite, Mode: rulescsv.Patch, Category: "test", Op: rulescsv.Delete, Site: &rulescsv.SiteRule{Type: rulescsv.Domain, Value: "example.com", Attrs: []string{"ads"}}}, {Dataset: rulescsv.GeoSite, Mode: rulescsv.Patch, Category: "test", Op: rulescsv.Add, Site: &rulescsv.SiteRule{Type: rulescsv.Domain, Value: "new.example"}}, {Dataset: rulescsv.GeoSite, Mode: rulescsv.Patch, Category: "missing", Op: rulescsv.Add, Site: &rulescsv.SiteRule{Type: rulescsv.Domain, Value: "skip.example"}, Source: rulescsv.SourceLocation{File: "missing.csv", Record: 2}}}
	out, result, err := ApplySite(input, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entry[0].Domain) != 2 {
		t.Fatalf("got %d domains", len(out.Entry[0].Domain))
	}
	if _, ok := out.Entry[0].Domain[0].Attribute[0].TypedValue.(*v2ray.Domain_Attribute_IntValue); !ok {
		t.Fatal("integer attribute lost")
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != "patch_category_missing" {
		t.Fatalf("warnings %#v", result.Warnings)
	}
	if len(input.Entry[0].Domain) != 2 {
		t.Fatal("input mutated")
	}
}

func TestApplySiteWildcardDelete(t *testing.T) {
	input := &v2ray.GeoSiteList{Entry: []*v2ray.GeoSite{{CountryCode: "x", Domain: []*v2ray.Domain{{Type: v2ray.Domain_Full, Value: "example.com"}, {Type: v2ray.Domain_Full, Value: "example.com", Attribute: []*v2ray.Domain_Attribute{{Key: "n", TypedValue: &v2ray.Domain_Attribute_IntValue{IntValue: 1}}}}}}}}
	records := []rulescsv.Record{{Dataset: rulescsv.GeoSite, Mode: rulescsv.Patch, Category: "x", Op: rulescsv.Delete, Site: &rulescsv.SiteRule{Type: rulescsv.Domain, Value: "example.com"}}}
	out, _, err := ApplySite(input, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entry[0].Domain) != 0 {
		t.Fatal("wildcard did not delete all variants")
	}
}

func TestApplyIPExactAndOverlap(t *testing.T) {
	p24 := netip.MustParsePrefix("192.0.2.0/24")
	p25 := netip.MustParsePrefix("192.0.2.0/25")
	input := &v2ray.GeoIPList{Entry: []*v2ray.GeoIP{{CountryCode: "x", ReverseMatch: true, Cidr: []*v2ray.CIDR{geodat.CIDR(p24)}}}}
	records := []rulescsv.Record{{Dataset: rulescsv.GeoIP, Mode: rulescsv.Patch, Category: "x", Op: rulescsv.Add, IP: &rulescsv.IPRule{Prefix: p25}}}
	out, _, err := ApplyIP(input, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entry[0].Cidr) != 2 || !out.Entry[0].ReverseMatch {
		t.Fatalf("unexpected output %#v", out.Entry[0])
	}
}
