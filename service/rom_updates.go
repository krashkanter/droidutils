package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ROM update discovery for the Redmi Note 9 (merlinx) running LineageOS.
//
// This only discovers and reports. It never downloads or flashes anything:
// the device runs a custom boot image and system_ext init integration (plus a
// Debian chroot hooked from init) that were built against a specific
// LineageOS build. Flashing any official build, even a newer one, replaces
// boot/system_ext and resets AVB/vbmeta, removing that integration.

const (
	romDevice          = "merlinx"
	romCustomImageDate = "20261110"
	romMaxBody         = 2 << 20
)

var (
	romBuildsURL   = "https://download.lineageos.org/api/v2/devices/" + romDevice + "/builds"
	romHTTPClient  = updateHTTP
	romGetprop     = "/system/bin/getprop"
	romDateInText  = regexp.MustCompile(`(?:^|[^0-9])(20[0-9]{6})(?:[^0-9]|$)`)
	romVersionText = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?`)
)

type romFile struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type romBuild struct {
	Date         string    `json:"date"`
	Datetime     int64     `json:"datetime"`
	Type         string    `json:"type"`
	Version      string    `json:"version"`
	OSPatchLevel string    `json:"os_patch_level"`
	Files        []romFile `json:"files"`
}

// day returns the build date as YYYYMMDD, or "" if unknown.
func (b romBuild) day() string {
	if d := strings.ReplaceAll(strings.TrimSpace(b.Date), "-", ""); len(d) >= 8 && isDigits(d[:8]) {
		return d[:8]
	}
	if b.Datetime > 0 {
		return time.Unix(b.Datetime, 0).UTC().Format("20060102")
	}
	for _, f := range b.Files {
		if m := romDateInText.FindStringSubmatch(f.Filename); m != nil {
			return m[1]
		}
	}
	return ""
}

func (b romBuild) label() string {
	v := strings.TrimSpace(b.Version)
	if v == "" {
		v = "unknown"
	}
	if d := b.day(); d != "" {
		v += " (" + d + ")"
	}
	return v
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// romMajorMinor parses "24.0" style versions; ok is false when unparseable.
func romMajorMinor(v string) (major, minor int, ok bool) {
	m := romVersionText.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		minor, _ = strconv.Atoi(m[2])
	}
	return major, minor, true
}

func romCompareVersion(a, b string) (int, bool) {
	am, an, ok1 := romMajorMinor(a)
	bm, bn, ok2 := romMajorMinor(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	switch {
	case am != bm:
		if am < bm {
			return -1, true
		}
		return 1, true
	case an != bn:
		if an < bn {
			return -1, true
		}
		return 1, true
	}
	return 0, true
}

func romGetProp(ctx context.Context, runner updateRunner, name string) (string, error) {
	out, err := runner(ctx, romGetprop, name)
	if err != nil {
		return "", fmt.Errorf("getprop %s: %w", name, err)
	}
	return strings.TrimSpace(out), nil
}

func fetchROMBuilds(ctx context.Context) ([]romBuild, error) {
	u, err := url.Parse(romBuildsURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, errors.New("refusing non-HTTPS ROM update source")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "droidutils-updater")
	resp, err := romHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LineageOS API returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, romMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > romMaxBody {
		return nil, errors.New("LineageOS API response too large")
	}
	var builds []romBuild
	if err := json.Unmarshal(body, &builds); err != nil {
		return nil, fmt.Errorf("decode LineageOS API response: %w", err)
	}
	return builds, nil
}

// newestROMBuild picks the newest build: highest version, then latest date.
func newestROMBuild(builds []romBuild) (romBuild, bool) {
	if len(builds) == 0 {
		return romBuild{}, false
	}
	s := append([]romBuild(nil), builds...)
	sort.SliceStable(s, func(i, j int) bool {
		if c, ok := romCompareVersion(s[i].Version, s[j].Version); ok && c != 0 {
			return c > 0
		}
		if s[i].day() != s[j].day() {
			return s[i].day() > s[j].day()
		}
		return s[i].Datetime > s[j].Datetime
	})
	return s[0], true
}

const romFlashWarning = "Automatic flashing is disabled. The installed custom boot image and system_ext init integration (which start the Debian chroot) were built for the LineageOS " + romCustomImageDate + " build. Flashing any official LineageOS build, even a newer one, overwrites the boot and system_ext partitions and resets AVB/vbmeta, removing that integration. Update manually: rebuild the custom images for the new build first, then flash them together."

func scanROM(ctx context.Context, runner updateRunner) updateItem {
	item := updateItem{ID: "rom", Name: "LineageOS ROM (" + romDevice + ")", Installable: false}

	device, err := romGetProp(ctx, runner, "ro.lineage.device")
	if err == nil && device == "" {
		device, err = romGetProp(ctx, runner, "ro.product.device")
	}
	if err != nil {
		item.Status, item.Detail = "error", "Could not read device identity: "+err.Error()
		return item
	}
	if device != romDevice {
		item.Current = device
		item.Status = "unavailable"
		item.Detail = fmt.Sprintf("ROM update discovery only supports %s; this device reports %q.", romDevice, device)
		return item
	}

	lineage, err := romGetProp(ctx, runner, "ro.lineage.version")
	if err != nil {
		item.Status, item.Detail = "error", "Could not read LineageOS version: "+err.Error()
		return item
	}
	if lineage == "" {
		item.Status, item.Detail = "unavailable", "ro.lineage.version is empty; this does not look like a LineageOS build."
		return item
	}
	curVer := lineage
	if v, _ := romGetProp(ctx, runner, "ro.lineage.build.version"); v != "" {
		curVer = v
	} else if i := strings.IndexByte(lineage, '-'); i > 0 {
		curVer = lineage[:i]
	}
	curDay := ""
	if m := romDateInText.FindStringSubmatch(lineage); m != nil {
		curDay = m[1]
	} else if s, _ := romGetProp(ctx, runner, "ro.build.date.utc"); s != "" {
		if n, e := strconv.ParseInt(s, 10, 64); e == nil && n > 0 {
			curDay = time.Unix(n, 0).UTC().Format("20060102")
		}
	}
	item.Current = curVer
	if curDay != "" {
		item.Current += " (" + curDay + ")"
	}

	builds, err := fetchROMBuilds(ctx)
	if err != nil {
		item.Status, item.Detail = "error", "Could not query official LineageOS builds: "+err.Error()
		return item
	}
	latest, ok := newestROMBuild(builds)
	if !ok {
		item.Status = "unavailable"
		item.Detail = "The official LineageOS API lists no builds for " + romDevice + ". " + romFlashWarning
		return item
	}
	item.Available = latest.label()
	latestDay := latest.day()
	desc := "Latest official build: LineageOS " + item.Available
	if latest.Type != "" {
		desc += " " + latest.Type
	}
	if latest.OSPatchLevel != "" {
		desc += ", security patch " + latest.OSPatchLevel
	}
	if len(latest.Files) > 0 && latest.Files[0].Filename != "" {
		desc += ", " + latest.Files[0].Filename
	}
	desc += ". "

	cmp, verOK := romCompareVersion(latest.Version, curVer)
	switch {
	case verOK && cmp < 0:
		item.Status = "current"
		item.Detail = desc + "The official channel is on an older LineageOS branch than the installed " + curVer + "; nothing newer to install. " + romFlashWarning
	case verOK && cmp == 0 && curDay != "" && latestDay != "" && latestDay <= curDay:
		item.Status = "current"
		item.Detail = desc + "Installed build is up to date. " + romFlashWarning
	case curDay == "" || latestDay == "" || !verOK:
		item.Status = "manual"
		item.Detail = desc + "Could not compare it reliably with the installed build. " + romFlashWarning
	default:
		item.Status = "manual"
		item.Detail = desc + "A newer official build exists. " + romFlashWarning
	}
	return item
}
