package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdaterRejectsUnconfirmedAndManualTargets(t *testing.T) {
	s := &Server{token: "test-token"}
	s.updates.report.Items = []updateItem{
		{ID: "rom", Available: "24.0 (20261117)"},
		{ID: "brave", Available: "1.100.58", Installable: true},
	}
	for _, tt := range []struct {
		body, token string
		want        int
	}{
		{`{"id":"brave","available":"1.100.58"}`, "", 401},
		{`{"id":"rom","available":"24.0 (20261117)"}`, "test-token", 409},
		{`{"id":"brave","available":"1.100.57"}`, "test-token", 409},
		{`{"id":"brave"}`, "test-token", 409},
		{`{"id":"anything","available":"1"}`, "test-token", 409},
	} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9474/v1/updates/apply", strings.NewReader(tt.body))
		req.Header.Set("Authorization", "Bearer "+tt.token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		s.ServeHTTP(response, req)
		if response.Code != tt.want {
			t.Fatalf("%s: got %d, want %d", tt.body, response.Code, tt.want)
		}
	}
}

func TestUpdaterCannotWriteOnAnotherDevice(t *testing.T) {
	count := 0
	run := func(_ context.Context, command string, args ...string) (string, error) {
		count++
		if command != "/system/bin/getprop" || len(args) != 1 || args[0] != "ro.product.device" {
			t.Fatalf("unexpected command: %s %q", command, args)
		}
		return "other-device", nil
	}
	for _, target := range []string{"debian", "brave", "hermes", "nginx"} {
		if err := applyUpdate(context.Background(), run, updateItem{ID: target}); err == nil {
			t.Fatalf("allowed %s on a different device", target)
		}
	}
	if count != 4 {
		t.Fatalf("unexpected command count %d", count)
	}
}

func TestComposeImageReplacementPreservesStorageAndOtherServices(t *testing.T) {
	original := []byte("services:\n  hermes:\n    image: nousresearch/hermes-agent:v0.23.2\n    volumes:\n      - kela-primary:/buckets/primary\n  other:\n    image: alpine:3.21\n")
	got, err := replaceComposeImages(original, "nousresearch/hermes-agent", "nousresearch/hermes-agent:v0.23.3", 1)
	want := strings.Replace(string(original), "v0.23.2", "v0.23.3", 1)
	if err != nil || string(got) != want {
		t.Fatalf("got %s (%v), want %s", got, err, want)
	}
	if _, err = replaceComposeImages(original, "nousresearch/hermes-agent", "nousresearch/hermes-agent:v0.23.3", 2); err == nil {
		t.Fatal("allowed a layout with the wrong number of managed services")
	}
}
