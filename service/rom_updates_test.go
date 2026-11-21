package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func romProps(p map[string]string, calls *[]string) updateRunner {
	return func(_ context.Context, name string, args ...string) (string, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		if name != "/system/bin/getprop" || len(args) != 1 {
			return "", errors.New("unexpected command")
		}
		return p[args[0]] + "\n", nil
	}
}

func merlinxProps(version string) map[string]string {
	return map[string]string{
		"ro.lineage.device":        "merlinx",
		"ro.lineage.version":       version,
		"ro.lineage.build.version": strings.SplitN(version, "-", 2)[0],
	}
}

func withROMAPI(t *testing.T, status int, payload string) *int {
	t.Helper()
	hits := new(int)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if r.URL.Path != "/api/v2/devices/merlinx/builds" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	oldURL, oldClient := romBuildsURL, romHTTPClient
	romBuildsURL, romHTTPClient = srv.URL+"/api/v2/devices/merlinx/builds", srv.Client()
	t.Cleanup(func() { romBuildsURL, romHTTPClient = oldURL, oldClient; srv.Close() })
	return hits
}

const romPayload = `[
 {"date":"2026-11-03","datetime":1793750400,"type":"nightly","version":"24.0","os_patch_level":"2026-11",
  "files":[{"filename":"lineage-24.0-20261103-nightly-merlinx-signed.zip","url":"https://mirror/x.zip"}]},
 {"date":"2026-11-17","datetime":1794960000,"type":"nightly","version":"24.0","os_patch_level":"2026-11",
  "files":[{"filename":"lineage-24.0-20261117-nightly-merlinx-signed.zip","url":"https://mirror/y.zip"}]},
 {"date":"2026-11-20","datetime":1795219200,"type":"nightly","version":"23.2",
  "files":[{"filename":"lineage-23.2-20261120-nightly-merlinx-signed.zip"}]}
]`

func TestScanROMNewerOfficialIsManualOnly(t *testing.T) {
	hits := withROMAPI(t, 200, romPayload)
	var calls []string
	it := scanROM(context.Background(), romProps(merlinxProps("24.0-20261110-UNOFFICIAL-merlinx"), &calls))
	if *hits != 1 {
		t.Fatalf("api hits %d", *hits)
	}
	if it.ID != "rom" || it.Status != "manual" || it.Installable {
		t.Fatalf("unexpected item %+v", it)
	}
	if it.Current != "24.0 (20261110)" || it.Available != "24.0 (20261117)" {
		t.Fatalf("versions %q -> %q", it.Current, it.Available)
	}
	for _, want := range []string{"20261110", "Automatic flashing is disabled", "boot", "system_ext", "AVB", "lineage-24.0-20261117-nightly-merlinx-signed.zip"} {
		if !strings.Contains(it.Detail, want) {
			t.Errorf("detail missing %q: %s", want, it.Detail)
		}
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "/system/bin/getprop ") {
			t.Errorf("unexpected command %q", c)
		}
	}
}

func TestScanROMCurrent(t *testing.T) {
	withROMAPI(t, 200, romPayload)
	var calls []string
	it := scanROM(context.Background(), romProps(merlinxProps("24.0-20261117-UNOFFICIAL-merlinx"), &calls))
	if it.Status != "current" || it.Installable || !strings.Contains(it.Detail, "20261110") {
		t.Fatalf("unexpected %+v", it)
	}
}

func TestScanROMOlderOfficialBranchIsCurrent(t *testing.T) {
	withROMAPI(t, 200, `[{"date":"2026-11-20","version":"23.2","files":[]}]`)
	var calls []string
	it := scanROM(context.Background(), romProps(merlinxProps("24.0-20261110-UNOFFICIAL-merlinx"), &calls))
	if it.Status != "current" || it.Available != "23.2 (20261120)" {
		t.Fatalf("unexpected %+v", it)
	}
}

func TestScanROMOtherDeviceSkipsNetwork(t *testing.T) {
	hits := withROMAPI(t, 200, romPayload)
	var calls []string
	p := map[string]string{"ro.product.device": "lavender", "ro.lineage.version": "24.0-20261110-UNOFFICIAL-lavender"}
	it := scanROM(context.Background(), romProps(p, &calls))
	if it.Status != "unavailable" || *hits != 0 || it.Installable {
		t.Fatalf("unexpected %+v hits=%d", it, *hits)
	}
}

func TestScanROMErrors(t *testing.T) {
	t.Run("getprop", func(t *testing.T) {
		withROMAPI(t, 200, romPayload)
		r := func(context.Context, string, ...string) (string, error) { return "", errors.New("boom") }
		if it := scanROM(context.Background(), r); it.Status != "error" {
			t.Fatalf("%+v", it)
		}
	})
	t.Run("http", func(t *testing.T) {
		withROMAPI(t, 500, "")
		var calls []string
		if it := scanROM(context.Background(), romProps(merlinxProps("24.0-20261110-UNOFFICIAL-merlinx"), &calls)); it.Status != "error" {
			t.Fatalf("%+v", it)
		}
	})
	t.Run("json", func(t *testing.T) {
		withROMAPI(t, 200, "{not json")
		var calls []string
		if it := scanROM(context.Background(), romProps(merlinxProps("24.0-20261110-UNOFFICIAL-merlinx"), &calls)); it.Status != "error" {
			t.Fatalf("%+v", it)
		}
	})
	t.Run("empty", func(t *testing.T) {
		withROMAPI(t, 200, "[]")
		var calls []string
		if it := scanROM(context.Background(), romProps(merlinxProps("24.0-20261110-UNOFFICIAL-merlinx"), &calls)); it.Status != "unavailable" {
			t.Fatalf("%+v", it)
		}
	})
	t.Run("plain-http-refused", func(t *testing.T) {
		old := romBuildsURL
		romBuildsURL = "http://download.lineageos.org/api/v2/devices/merlinx/builds"
		defer func() { romBuildsURL = old }()
		var calls []string
		it := scanROM(context.Background(), romProps(merlinxProps("24.0-20261110-UNOFFICIAL-merlinx"), &calls))
		if it.Status != "error" || !strings.Contains(it.Detail, "HTTPS") {
			t.Fatalf("%+v", it)
		}
	})
}
