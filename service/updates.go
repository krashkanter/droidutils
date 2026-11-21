package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const debian = "/data/local/server/debian"

type updateRunner func(context.Context, string, ...string) (string, error)

type updateItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Current     string `json:"current"`
	Available   string `json:"available"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
	Installable bool   `json:"installable"`
	target      string
}

type updateReport struct {
	Running   bool         `json:"running"`
	Operation string       `json:"operation"`
	Error     string       `json:"error,omitempty"`
	Items     []updateItem `json:"items"`
}

type updateState struct {
	mu     sync.Mutex
	report updateReport
}

func runUpdate(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	output := &tailWriter{}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	return strings.TrimSpace(string(output.bytes())), err
}

func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request, parts []string) {
	u := &s.updates
	if len(parts) == 2 && r.Method == http.MethodGet {
		u.mu.Lock()
		defer u.mu.Unlock()
		report := u.report
		if report.Items == nil {
			report.Items = []updateItem{}
		}
		writeJSON(w, 200, report)
		return
	}
	if len(parts) != 3 || r.Method != http.MethodPost || (parts[2] != "check" && parts[2] != "apply") {
		fail(w, 405, "Unsupported update action")
		return
	}
	var request struct {
		ID        string `json:"id"`
		Available string `json:"available"`
	}
	if parts[2] == "apply" && !decode(w, r, &request) {
		return
	}
	u.mu.Lock()
	if u.report.Running {
		u.mu.Unlock()
		fail(w, 409, "An update operation is already running")
		return
	}
	var item updateItem
	if parts[2] == "apply" {
		for _, candidate := range u.report.Items {
			if candidate.ID == request.ID && candidate.Installable && candidate.Available == request.Available {
				item = candidate
				break
			}
		}
		if item.ID == "" {
			u.mu.Unlock()
			fail(w, 409, "Run a check and confirm the listed version before updating")
			return
		}
	}
	u.report.Running = true
	u.report.Operation = "Checking for updates"
	if item.ID != "" {
		u.report.Operation = "Updating " + item.Name
	}
	u.report.Error = ""
	u.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
		defer cancel()
		var err error
		if item.ID != "" {
			err = applyUpdate(ctx, runUpdate, item)
		}
		items := scanUpdates(ctx, runUpdate, item.ID == "")
		u.mu.Lock()
		defer u.mu.Unlock()
		u.report = updateReport{Items: items}
		if item.ID != "" && err == nil {
			u.report.Operation = "Updated " + item.Name
			if item.ID == "debian" {
				u.report.Operation += ". Restart the phone to reload running services."
			}
		}
		if err != nil {
			u.report.Error = err.Error()
		}
	}()
	writeJSON(w, 202, map[string]bool{"queued": true})
}

func scanUpdates(ctx context.Context, run updateRunner, refresh bool) []updateItem {
	items := []updateItem{scanROM(ctx, run)}
	device, _ := run(ctx, "/system/bin/getprop", "ro.product.device")
	// All write operations in this profile are specific to the user's merlinx layout.
	if device != "merlinx" {
		return items
	}
	items = append(items, scanDebian(ctx, run, refresh))
	for _, spec := range managedImages {
		items = append(items, scanImage(ctx, run, spec))
	}
	items = append(items, scanCopyparty(ctx))
	return append(items, scanApps(ctx, run)...)
}

func scanCopyparty(ctx context.Context) updateItem {
	i := updateItem{ID: "copyparty", Name: "copyparty file server", Status: "unavailable"}
	path := "/data/local/debian/opt/kela-storage/copyparty-sfx.py"
	f, err := os.Open(path)
	if err != nil {
		i.Detail = "Native storage server is not provisioned."
		return i
	}
	defer f.Close()
	// Recognize the exact installed Kela Server artifact without executing it.
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, 8<<20)); err == nil &&
		fmt.Sprintf("%x", h.Sum(nil)) == "8f9a0def8a9c15252a06395781ec083372043b60caa80fb33c3d6dafa7f1b40a" {
		i.Current = "1.20.26"
	}
	version, _, err := githubRelease(ctx, "9001/copyparty")
	if err != nil {
		i.Status, i.Detail = "error", "Could not reach the copyparty release feed."
		return i
	}
	i.Available = version
	i.Status, i.Detail = "manual", "Use the Kela Server storage deployment to update the native file server."
	if version == i.Current {
		i.Status, i.Detail = "current", "The installed file matches the current upstream release."
	}
	return i
}

func scanDebian(ctx context.Context, run updateRunner, refresh bool) updateItem {
	i := updateItem{ID: "debian", Name: "Debian packages (chroot)", Status: "error"}
	if _, err := os.Stat(debian); err != nil {
		i.Status, i.Detail = "unavailable", "Debian chroot wrapper is absent."
		return i
	}
	i.Current, _ = run(ctx, debian, "cat", "/etc/debian_version")
	if refresh {
		if _, err := run(ctx, debian, "apt-get", "-o", "APT::Update::Error-Mode=any", "update"); err != nil {
			i.Detail = "Could not refresh the Debian repositories."
			return i
		}
	}
	output, err := run(ctx, debian, "apt-get", "-s", "--no-remove", "upgrade")
	if err != nil {
		i.Detail = "Debian could not simulate the upgrade. Check dpkg and repository health."
		return i
	}
	var packages []string
	var expected []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == "Inst" {
			version := strings.TrimPrefix(fields[3], "(")
			if !strings.HasPrefix(fields[2], "[") || !strings.HasPrefix(fields[3], "(") || version == "" {
				i.Detail = "The proposed package plan could not be verified."
				return i
			}
			packages = append(packages, fields[1])
			expected = append(expected, fields[1]+"="+version)
		}
	}
	if len(packages) == 0 {
		i.Status, i.Detail = "current", "No in-place upgrade is available."
		return i
	}
	i.Status, i.Installable = "available", true
	// Bind confirmation to the candidate package versions, not just the count.
	i.target = strings.Join(expected, "\n")
	plan := sha256.Sum256([]byte(i.target))
	i.Available = fmt.Sprintf("%d package updates (%x)", len(packages), plan[:6])
	i.Detail = strings.Join(packages, ", ") + ". Restart the phone afterward to reload running services."
	return i
}

const aptUpgrade = `set -eu
policy=/usr/sbin/policy-rc.d
created=0
trap 'if [ "$created" = 1 ]; then rm -f "$policy"; fi' EXIT
if [ ! -e "$policy" ] && [ ! -L "$policy" ]; then
  (set -C; printf '%s\n' '#!/bin/sh' 'exit 101' > "$policy")
  created=1
  chmod 755 "$policy"
fi
DEBIAN_FRONTEND=noninteractive apt-get -y --no-remove --no-install-recommends --only-upgrade -o Dpkg::Options::=--force-confold install "$@"
test -z "$(dpkg --audit)"
`

var updateHTTP = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 5 {
			return errors.New("invalid update redirect")
		}
		return nil
	},
}

// The root daemon is a static Linux binary, which cannot use Android's
// resolver via libc. Use the server chroot's configured DNS for update requests.
func configureUpdateDNS(root string) {
	data, err := os.ReadFile(root + "/etc/resolv.conf")
	if err != nil {
		return
	}
	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" && net.ParseIP(fields[1]) != nil {
			servers = append(servers, net.JoinHostPort(fields[1], "53"))
		}
	}
	if len(servers) == 0 {
		return
	}
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: 3 * time.Second}
		var last error
		for _, server := range servers {
			conn, err := dialer.DialContext(ctx, network, server)
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Resolver: resolver, Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	updateHTTP.Transport = transport
}

func updateJSON(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "DroidUtils-updater")
	response, err := updateHTTP.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("update source returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(http.MaxBytesReader(nil, response.Body, 2<<20)).Decode(into)
}

type imageSpec struct {
	id, name, repo, tag, compose, container string
	services                                []string
}

var managedImages = []imageSpec{
	{"nginx", "nginx gateway", "library/nginx", "stable-alpine", "/opt/phone-gateway/compose.yaml", "phone-gateway-gateway-1", []string{"hello", "example", "gateway"}},
	{"cloudflared", "Cloudflare tunnel", "cloudflare/cloudflared", "latest", "/opt/phone-gateway/compose.yaml", "phone-gateway-cloudflare-1", []string{"cloudflare"}},
	{"hermes", "Hermes Agent", "nousresearch/hermes-agent", "", "/opt/hermes/compose.yaml", "phone-hermes", []string{"hermes"}},
}
var imageDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var stableTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

func scanImage(ctx context.Context, run updateRunner, spec imageSpec) updateItem {
	i := updateItem{ID: spec.id, Name: spec.name, Status: "unavailable"}
	container, err := run(ctx, debian, "docker", "inspect", "--format", "{{.Image}} {{.Config.Image}} {{.State.Running}}", spec.container)
	fields := strings.Fields(container)
	if err != nil || len(fields) != 3 || fields[2] != "true" {
		i.Detail = "The managed container is not running."
		return i
	}
	i.Current = fields[1]
	tag := spec.tag
	if tag == "" {
		var release struct {
			Tag string `json:"tag_name"`
		}
		if updateJSON(ctx, "https://api.github.com/repos/NousResearch/hermes-agent/releases/latest", &release) != nil || !stableTag.MatchString(release.Tag) {
			i.Status, i.Detail = "error", "Could not check the stable Hermes release."
			return i
		}
		tag = release.Tag
	}
	var remote struct {
		Digest string `json:"digest"`
	}
	if updateJSON(ctx, "https://hub.docker.com/v2/repositories/"+spec.repo+"/tags/"+tag, &remote) != nil || !imageDigest.MatchString(remote.Digest) {
		i.Status, i.Detail = "error", "Could not verify the registry tag digest."
		return i
	}
	local, err := run(ctx, debian, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", fields[0])
	if err != nil {
		i.Status, i.Detail = "error", "Could not inspect the running image."
		return i
	}
	var digests []string
	if json.Unmarshal([]byte(local), &digests) != nil {
		i.Status, i.Detail = "error", "Could not read the installed image digest."
		return i
	}
	i.Available = tag + " (" + remote.Digest[7:19] + ")"
	i.target = tag + "@" + remote.Digest
	for _, digest := range digests {
		if strings.HasSuffix(digest, "@"+remote.Digest) {
			i.Status, i.Detail = "current", "Running image matches the registry."
			return i
		}
	}
	i.Status, i.Detail, i.Installable = "available", "Recreates the managed Compose service and keeps its existing storage mounts.", true
	return i
}

func applyUpdate(ctx context.Context, run updateRunner, item updateItem) error {
	device, err := run(ctx, "/system/bin/getprop", "ro.product.device")
	if err != nil || device != "merlinx" {
		return errors.New("This update profile requires merlinx")
	}
	if item.ID == "debian" {
		latest := scanDebian(ctx, run, false)
		if !latest.Installable || latest.target != item.target {
			return errors.New("Debian package candidates changed. Check again.")
		}
		args := append([]string{"sh", "-c", aptUpgrade, "droidutils-apt"}, strings.Split(item.target, "\n")...)
		if _, err := run(ctx, debian, args...); err != nil {
			return errors.New("Debian upgrade failed. Check dpkg and APT logs.")
		}
		return nil
	}
	if item.ID == "brave" {
		return applyBrave(ctx, run, item)
	}
	for _, spec := range managedImages {
		if spec.id == item.ID {
			return applyImage(ctx, run, spec, item)
		}
	}
	return errors.New("This component needs a manual update")
}

func applyImage(ctx context.Context, run updateRunner, spec imageSpec, item updateItem) error {
	latest := scanImage(ctx, run, spec)
	if !latest.Installable || latest.target != item.target {
		return errors.New("Container candidate changed. Check again.")
	}
	repo := strings.TrimPrefix(spec.repo, "library/")
	ref := repo + ":" + item.target
	var previousID string
	for _, name := range managedContainerNames(spec) {
		out, err := run(ctx, debian, "docker", "inspect", "--format", "{{.Image}} {{.State.Running}}", name)
		fields := strings.Fields(out)
		if err != nil || len(fields) != 2 || !imageDigest.MatchString(fields[0]) ||
			fields[1] != "true" || (previousID != "" && previousID != fields[0]) {
			return errors.New("Expected healthy services with a shared previous image. Check the Compose project.")
		}
		previousID = fields[0]
	}
	if _, err := run(ctx, debian, "docker", "pull", ref); err != nil {
		return errors.New("Could not pull the verified container image")
	}
	// Use the Compose config in the chroot and preserve its mounts and settings.
	path := "/data/local/debian" + spec.compose
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("Could not read the managed Compose file")
	}
	expected := len(spec.services)
	if spec.id == "cloudflared" {
		expected = 2
	} // Named and optional quick tunnel share the image.
	next, err := replaceComposeImages(data, repo, ref, expected)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	// Persist the original before the replacement so owner recovery survives daemon loss.
	if err := atomic(filepath.Join(filepath.Dir(path), "compose.before-droidutils.yaml"), data, 0600); err != nil {
		return err
	}
	if err := atomic(path, next, info.Mode().Perm()); err != nil {
		return err
	}
	args := []string{"docker", "compose", "-f", spec.compose, "up", "-d", "--no-deps", "--pull", "never"}
	args = append(args, spec.services...)
	if _, err := run(ctx, debian, args...); err == nil {
		for n := 0; n < 3; n++ {
			for _, name := range managedContainerNames(spec) {
				running, err := run(ctx, debian, "docker", "inspect", "--format", "{{.State.Running}} {{.Config.Image}}", name)
				if err != nil || running != "true "+ref {
					goto rollbackImage
				}
			}
			if n == 2 {
				return nil
			}
			time.Sleep(2 * time.Second)
		}
	}
rollbackImage:
	if err := atomic(path, data, info.Mode().Perm()); err != nil {
		return errors.New("Update failed and the prior Compose file could not be restored")
	}
	rollback, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rollbackRef := repo + ":droidutils-rollback-" + previousID[7:19]
	if _, err := run(rollback, debian, "docker", "image", "tag", previousID, rollbackRef); err != nil {
		return errors.New("Update failed. Prior Compose file restored, but the earlier image could not be tagged.")
	}
	restore, err := replaceComposeImages(data, repo, rollbackRef, expected)
	if err != nil || atomic(path, restore, info.Mode().Perm()) != nil {
		return errors.New("Update failed. Could not pin the earlier image.")
	}
	if _, err := run(rollback, debian, args...); err != nil {
		return errors.New("Update failed. Prior Compose config restored, but service restart failed.")
	}
	return errors.New("Container update failed. Rolled back the managed services to their previous image.")
}

func managedContainerNames(spec imageSpec) []string {
	if spec.id != "nginx" {
		return []string{spec.container}
	}
	return []string{"phone-gateway-hello-1", "phone-gateway-example-1", spec.container}
}

func replaceComposeImages(data []byte, repo, ref string, expected int) ([]byte, error) {
	lines := strings.Split(string(data), "\n")
	changed := 0
	for n, line := range lines {
		image := strings.TrimSpace(line)
		if !strings.HasPrefix(image, "image: "+repo+":") {
			continue
		}
		oldRef := strings.TrimPrefix(image, "image: ")
		if !managedImageReference(oldRef, repo) {
			return nil, errors.New("Compose image does not match the managed format")
		}
		lines[n] = strings.Replace(line, oldRef, ref, 1)
		changed++
	}
	if changed != expected {
		return nil, errors.New("Compose file differs from the managed server layout")
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func managedImageReference(ref, repo string) bool {
	if !strings.HasPrefix(ref, repo+":") {
		return false
	}
	tag, digest, pinned := strings.Cut(strings.TrimPrefix(ref, repo+":"), "@")
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(tag) {
		return false
	}
	return !pinned || imageDigest.MatchString(digest)
}

var androidVersion = regexp.MustCompile(`(?m)^\s*versionName=([^\r\n]+)`)

type githubAsset struct {
	Name   string
	Digest string
	Size   int64
	URL    string `json:"browser_download_url"`
}

func githubRelease(ctx context.Context, repo string) (string, []githubAsset, error) {
	var result struct {
		Tag    string `json:"tag_name"`
		Assets []githubAsset
	}
	err := updateJSON(ctx, "https://api.github.com/repos/"+repo+"/releases/latest", &result)
	return strings.TrimPrefix(result.Tag, "v"), result.Assets, err
}

func installedApp(ctx context.Context, run updateRunner, pkg string) string {
	out, _ := run(ctx, "/system/bin/dumpsys", "package", pkg)
	if match := androidVersion.FindStringSubmatch(out); match != nil {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func newerApp(current, candidate string) bool {
	parse := func(v string) ([]int, bool) {
		var parts []int
		for _, part := range strings.Split(v, ".") {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || !isDigits(part) {
				return nil, false
			}
			parts = append(parts, n)
		}
		return parts, true
	}
	a, ok := parse(current)
	b, valid := parse(candidate)
	if !ok || !valid || len(a) != len(b) {
		return false
	}
	for n := range a {
		if a[n] != b[n] {
			return b[n] > a[n]
		}
	}
	return false
}

func braveRelease(ctx context.Context) (string, githubAsset, error) {
	version, assets, err := githubRelease(ctx, "brave/brave-browser")
	if err != nil {
		return "", githubAsset{}, err
	}
	for _, file := range assets {
		if file.Name == "Bravearm64Universal.apk" && imageDigest.MatchString(file.Digest) &&
			file.URL == "https://github.com/brave/brave-browser/releases/download/v"+version+"/"+file.Name &&
			file.Size > 0 && file.Size <= 300<<20 {
			return version, file, nil
		}
	}
	return "", githubAsset{}, errors.New("No verified publisher ARM64 APK")
}

func scanApps(ctx context.Context, run updateRunner) []updateItem {
	apps := []struct{ id, name, pkg, repo, detail string }{
		{"brave", "Brave", "com.brave.browser", "", ""},
		{"localsend", "LocalSend", "org.localsend.localsend_app", "localsend/localsend", "Use its official release APK."},
		{"lawnicons", "Lawnicons", "app.lawnchair.lawnicons.play", "LawnchairLauncher/lawnicons", "Use the existing app store channel."},
		{"vlc", "VLC", "org.videolan.vlc", "", "Use the existing app store channel or VideoLAN’s APK archive."},
		{"lawnchair", "Lawnchair", "app.lawnchair.play", "", "Check the current beta channel in the app store."},
		{"aurora", "Aurora Store", "com.aurora.store", "", "Check the Aurora Store release channel."},
		{"zarchiver", "ZArchiver", "ru.zdevs.zarchiver", "", "Check ZDevs or the existing store channel."},
		{"kela", "Kela", "com.krashkanter.kela", "", "Update the app and server bridge from the Kela project."},
		{"droidutils", "DroidUtils", "com.krashkanter.droidutils", "", "Install the matching APK and root daemon from the DroidUtils release."},
		{"ir", "OPPO Remote", "com.oplus.consumerIRApp", "", "No compatible publisher update feed is configured."},
	}
	var list []updateItem
	for _, app := range apps {
		current := installedApp(ctx, run, app.pkg)
		if current == "" {
			continue
		}
		i := updateItem{ID: app.id, Name: app.name, Current: current, Status: "manual", Detail: app.detail}
		if app.id == "brave" {
			version, file, err := braveRelease(ctx)
			if err != nil {
				i.Status, i.Detail = "error", "Could not verify Brave's release."
			} else {
				i.Available, i.target = version, file.Digest
				if newerApp(current, version) {
					i.Status, i.Installable, i.Detail = "available", true, "Installs a verified ARM64 APK in place. Android checks its signing certificate and may close Brave."
				} else {
					i.Status, i.Detail = "current", "No newer compatible release was found."
				}
			}
		} else if app.repo != "" {
			version, _, err := githubRelease(ctx, app.repo)
			if err != nil {
				i.Status, i.Detail = "error", "Could not reach the upstream release feed."
			} else {
				i.Available = version
				if current == version {
					i.Status, i.Detail = "current", "Installed version matches the upstream release."
				}
			}
		}
		list = append(list, i)
	}
	return list
}

func applyBrave(ctx context.Context, run updateRunner, item updateItem) error {
	version, asset, err := braveRelease(ctx)
	if err != nil || version != item.Available || asset.Digest != item.target || !newerApp(installedApp(ctx, run, "com.brave.browser"), version) {
		return errors.New("Brave release or installed version changed. Check again.")
	}
	file, err := os.CreateTemp("/data/local/tmp", "droidutils-brave-*.apk")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	dl := *updateHTTP
	dl.Timeout = 8 * time.Minute
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	response, err := dl.Do(req)
	if err != nil {
		return errors.New("Brave download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("Brave download returned HTTP %d", response.StatusCode)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, asset.Size+1))
	if err != nil || n != asset.Size || fmt.Sprintf("sha256:%x", hash.Sum(nil)) != asset.Digest {
		return errors.New("Brave APK size or checksum did not match")
	}
	if err := file.Close(); err != nil {
		return err
	}
	out, err := run(ctx, "/system/bin/pm", "install", "-r", file.Name())
	if err != nil || strings.TrimSpace(out) != "Success" {
		return errors.New("Android rejected the in-place Brave update")
	}
	if installedApp(ctx, run, "com.brave.browser") != version {
		return errors.New("Installed Brave version could not be verified")
	}
	return nil
}
