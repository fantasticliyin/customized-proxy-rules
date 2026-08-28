// SPDX-License-Identifier: GPL-3.0-only
package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveAndDownloadVerified(t *testing.T) {
	data := []byte("protobuf fixture")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	checksumData := []byte(digest + "  geosite.dat\n")
	checksumSum := sha256.Sum256(checksumData)
	checksumDigest := hex.EncodeToString(checksumSum[:])
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/MetaCubeX/meta-rules-dat/releases/42":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":42,"tag_name":"latest","published_at":"2026-08-27T00:00:00Z","assets":[{"id":1,"name":"geosite.dat","size":%d,"digest":"sha256:%s","browser_download_url":"%s/data"},{"id":2,"name":"geosite.dat.sha256sum","size":%d,"digest":"sha256:%s","browser_download_url":"%s/sum"}]}`, len(data), digest, server.URL, len(checksumData), checksumDigest, server.URL)
		case "/data":
			_, _ = w.Write(data)
		case "/sum":
			_, _ = w.Write(checksumData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient("")
	client.APIBase = server.URL
	client.HTTP = server.Client()
	client.HTTP.Timeout = time.Second
	release, err := client.Resolve(context.Background(), "MetaCubeX/meta-rules-dat", "42")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := SelectAssets(release, []string{"geosite.dat", "geosite.dat.sha256sum"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.DownloadVerified(context.Background(), assets["geosite.dat"], assets["geosite.dat.sha256sum"], filepath.Join(t.TempDir(), "geosite.dat"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got != digest {
		t.Fatalf("digest got %s", got)
	}
}

func TestDownloadRejectsDigestDisagreement(t *testing.T) {
	checksumData := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  data\n")
	checksumSum := sha256.Sum256(checksumData)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(checksumData)
	}))
	defer server.Close()
	client := NewClient("")
	client.HTTP = server.Client()
	_, err := client.DownloadVerified(context.Background(), Asset{ID: 1, Name: "data", Size: 1, Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", BrowserDownloadURL: server.URL}, Asset{ID: 2, Name: "sum", Size: int64(len(checksumData)), Digest: "sha256:" + hex.EncodeToString(checksumSum[:]), BrowserDownloadURL: server.URL}, filepath.Join(t.TempDir(), "data"), 100)
	if err == nil {
		t.Fatal("digest disagreement accepted")
	}
}
