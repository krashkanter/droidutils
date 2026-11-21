package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Script struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	Enabled bool   `json:"enabled"`
	Order   int    `json:"order"`
	Timeout int    `json:"timeout"`
}
type Result struct {
	Running  bool   `json:"running"`
	Started  string `json:"started,omitempty"`
	Finished string `json:"finished,omitempty"`
	Exit     int    `json:"exit"`
	Error    string `json:"error,omitempty"`
}
type Entry struct {
	Script
	Result Result `json:"result"`
}
type running struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type Server struct {
	root, token, bootID string
	mu                  sync.Mutex
	scripts             map[string]Script
	results             map[string]Result
	active              map[string]running
	slots               chan struct{}
	updates             updateState
}

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,160}$`)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, err string) {
	writeJSON(w, code, map[string]string{"error": err})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "Invalid JSON: "+err.Error())
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "Expected one JSON object")
		return false
	}
	return true
}
func atomic(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	return err
}
func NewServer(root string) (*Server, error) {
	for _, d := range []string{root, filepath.Join(root, "scripts"), filepath.Join(root, "logs"), filepath.Join(root, "results")} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return nil, err
		}
		if err := os.Chmod(d, 0700); err != nil {
			return nil, err
		}
	}
	tokenPath := filepath.Join(root, "token")
	b, err := os.ReadFile(tokenPath)
	if errors.Is(err, os.ErrNotExist) {
		b = make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return nil, err
		}
		b = []byte(hex.EncodeToString(b))
		err = atomic(tokenPath, b, 0600)
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) != 64 {
		return nil, errors.New("invalid token file")
	}
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	s := &Server{root: root, token: strings.TrimSpace(string(b)), bootID: strings.TrimSpace(string(boot)), scripts: map[string]Script{}, results: map[string]Result{}, active: map[string]running{}, slots: make(chan struct{}, 4)}
	files, err := os.ReadDir(filepath.Join(root, "scripts"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		var v Script
		b, e := os.ReadFile(filepath.Join(root, "scripts", f.Name()))
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &v); e != nil {
			return nil, e
		}
		if !idPattern.MatchString(v.ID) || f.Name() != v.ID+".json" {
			return nil, errors.New("invalid script identity")
		}
		if e = validate(v); e != nil {
			return nil, e
		}
		s.scripts[v.ID] = v
		var result Result
		if b, e = os.ReadFile(filepath.Join(root, "results", v.ID+".json")); e == nil && json.Unmarshal(b, &result) == nil {
			if result.Running {
				result.Running = false
				result.Error = "Interrupted by daemon exit or reboot"
				result.Exit = -1
			}
			s.results[v.ID] = result
		}
	}
	return s, nil
}
func validate(v Script) error {
	if len(strings.TrimSpace(v.Name)) == 0 || len(v.Name) > 120 {
		return errors.New("Name must be 1–120 bytes")
	}
	if len(v.Body) == 0 || len(v.Body) > 128*1024 || strings.ContainsRune(v.Body, 0) {
		return errors.New("Script must contain 1–131072 bytes without NUL")
	}
	if v.Timeout < 1 || v.Timeout > 3600 {
		return errors.New("Timeout must be 1–3600 seconds")
	}
	if v.Order < 0 || v.Order > 9999 {
		return errors.New("Order must be 0–9999")
	}
	return nil
}
func (s *Server) save(v Script) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	return atomic(filepath.Join(s.root, "scripts", v.ID+".json"), b, 0600)
}
func (s *Server) list() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]Entry, 0, len(s.scripts))
	for id, v := range s.scripts {
		rows = append(rows, Entry{v, s.results[id]})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Order == rows[j].Order {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Order < rows[j].Order
	})
	return rows
}
func (s *Server) result(id string, r Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[id] = r
	b, _ := json.Marshal(r)
	if err := atomic(filepath.Join(s.root, "results", id+".json"), b, 0600); err != nil {
		log.Print(err)
	}
}

type tailWriter struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	t.b = append(t.b, p...)
	if len(t.b) > 128*1024 {
		t.b = append([]byte{}, t.b[len(t.b)-128*1024:]...)
	}
	return n, nil
}
func (t *tailWriter) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte{}, t.b...)
}
func (s *Server) run(id string) (<-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.scripts[id]
	if !ok {
		return nil, errors.New("Script not found")
	}
	if _, ok = s.active[id]; ok {
		return nil, errors.New("Already running")
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return nil, errors.New("Four scripts are already running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(v.Timeout)*time.Second)
	done := make(chan struct{})
	s.active[id] = running{cancel, done}
	result := Result{Running: true, Started: time.Now().UTC().Format(time.RFC3339), Exit: -1}
	s.results[id] = result
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.active, id)
			s.mu.Unlock()
			<-s.slots
			close(done)
		}()
		shell := "/system/bin/sh"
		if _, e := os.Stat(shell); e != nil {
			shell = "/bin/sh"
		}
		cmd := exec.CommandContext(ctx, shell, "-s")
		cmd.Stdin = strings.NewReader(v.Body)
		cmd.Dir = s.root
		cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin", "HOME="+s.root, "SYSUTILS_SCRIPT_ID="+id, "SYSUTILS_BOOT_ID="+s.bootID)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			return nil
		}
		cmd.WaitDelay = 2 * time.Second
		writer := &tailWriter{}
		cmd.Stdout = writer
		cmd.Stderr = writer
		logPath := filepath.Join(s.root, "logs", id+".log")
		_ = atomic(logPath, nil, 0600)
		s.result(id, result)
		progressDone := make(chan struct{})
		progressStopped := make(chan struct{})
		go func() {
			defer close(progressStopped)
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-progressDone:
					return
				case <-tick.C:
					_ = atomic(logPath, writer.bytes(), 0600)
				}
			}
		}()
		err := cmd.Run()
		close(progressDone)
		<-progressStopped
		if ctx.Err() != nil && cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		result.Running = false
		result.Finished = time.Now().UTC().Format(time.RFC3339)
		result.Exit = 0
		if err != nil {
			result.Exit = -1
			var ex *exec.ExitError
			if errors.As(err, &ex) {
				result.Exit = ex.ExitCode()
			}
			result.Error = err.Error()
		}
		if ctx.Err() != nil {
			result.Error = ctx.Err().Error()
			result.Exit = -1
		}
		_ = atomic(logPath, writer.bytes(), 0600)
		s.result(id, result)
	}()
	return done, nil
}
func (s *Server) boot() {
	if s.bootID == "" {
		log.Print("No boot ID; startup execution refused")
		return
	}
	marker := filepath.Join(s.root, "last-boot")
	b, _ := os.ReadFile(marker)
	if string(b) == s.bootID {
		return
	}
	if err := atomic(marker, []byte(s.bootID), 0600); err != nil {
		log.Print(err)
		return
	}
	for _, v := range s.list() {
		if !v.Enabled {
			continue
		}
		done, err := s.run(v.ID)
		if err != nil {
			log.Print(err)
			continue
		}
		<-done
	}
}
func command(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(b)), err
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		fail(w, 403, "Browser origins are not permitted")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
		fail(w, 401, "Unauthorized")
		return
	}
	if r.Host != "127.0.0.1:9474" {
		fail(w, 403, "Invalid host")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "v1" {
		fail(w, 404, "Not found")
		return
	}
	if len(parts) == 2 && parts[1] == "status" && r.Method == "GET" {
		u, _ := os.ReadFile("/proc/uptime")
		memory, _ := os.ReadFile("/proc/meminfo")
		writeJSON(w, 200, map[string]any{"uid": os.Getuid(), "pid": os.Getpid(), "boot_id": s.bootID, "uptime": strings.Fields(string(u)), "memory": string(memory), "scripts": len(s.list()), "root": s.root})
		return
	}
	if parts[1] == "scripts" {
		s.handleScripts(w, r, parts)
		return
	}
	if parts[1] == "settings" && len(parts) == 2 {
		s.handleSettings(w, r)
		return
	}
	if parts[1] == "services" {
		s.handleServices(w, r, parts)
		return
	}
	if parts[1] == "updates" {
		s.handleUpdates(w, r, parts)
		return
	}
	fail(w, 404, "Not found")
}
func (s *Server) handleScripts(w http.ResponseWriter, r *http.Request, p []string) {
	if len(p) == 2 {
		switch r.Method {
		case "GET":
			writeJSON(w, 200, s.list())
			return
		case "POST":
			var v Script
			if !decode(w, r, &v) {
				return
			}
			if v.ID != "" {
				fail(w, 400, "ID is assigned by server")
				return
			}
			if err := validate(v); err != nil {
				fail(w, 400, err.Error())
				return
			}
			id := make([]byte, 16)
			if _, err := rand.Read(id); err != nil {
				fail(w, 500, "Entropy unavailable")
				return
			}
			v.ID = hex.EncodeToString(id)
			s.mu.Lock()
			err := s.save(v)
			if err == nil {
				s.scripts[v.ID] = v
			}
			s.mu.Unlock()
			if err != nil {
				fail(w, 500, err.Error())
				return
			}
			writeJSON(w, 201, v)
			return
		}
	}
	if len(p) < 3 || !idPattern.MatchString(p[2]) {
		fail(w, 404, "Not found")
		return
	}
	id := p[2]
	if len(p) == 4 {
		switch {
		case p[3] == "run" && r.Method == "POST":
			_, err := s.run(id)
			if err != nil {
				fail(w, 409, err.Error())
				return
			}
			writeJSON(w, 202, map[string]bool{"ok": true})
			return
		case p[3] == "stop" && r.Method == "POST":
			s.mu.Lock()
			a, ok := s.active[id]
			if ok {
				a.cancel()
			}
			s.mu.Unlock()
			if !ok {
				fail(w, 409, "Not running")
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		case p[3] == "logs" && r.Method == "GET":
			s.mu.Lock()
			_, exists := s.scripts[id]
			result := s.results[id]
			s.mu.Unlock()
			if !exists {
				fail(w, 404, "Not found")
				return
			}
			b, err := os.ReadFile(filepath.Join(s.root, "logs", id+".log"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				fail(w, 500, err.Error())
				return
			}
			writeJSON(w, 200, map[string]any{"text": string(b), "result": result})
			return
		}
		fail(w, 405, "Unsupported action")
		return
	}
	if len(p) != 3 {
		fail(w, 404, "Not found")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, exists := s.scripts[id]
	if !exists {
		fail(w, 404, "Not found")
		return
	}
	if r.Method == "GET" {
		writeJSON(w, 200, Entry{v, s.results[id]})
		return
	}
	if _, ok := s.active[id]; ok {
		fail(w, 409, "Stop the running script first")
		return
	}
	switch r.Method {
	case "PUT":
		var next Script
		if !decode(w, r, &next) {
			return
		}
		if next.ID != "" && next.ID != id {
			fail(w, 400, "ID mismatch")
			return
		}
		next.ID = id
		if err := validate(next); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if err := s.save(next); err != nil {
			fail(w, 500, err.Error())
			return
		}
		s.scripts[id] = next
		writeJSON(w, 200, next)
	case "DELETE":
		if err := os.Remove(filepath.Join(s.root, "scripts", id+".json")); err != nil {
			fail(w, 500, err.Error())
			return
		}
		delete(s.scripts, id)
		delete(s.results, id)
		_ = os.Remove(filepath.Join(s.root, "logs", id+".log"))
		_ = os.Remove(filepath.Join(s.root, "results", id+".json"))
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 405, "Method not allowed")
	}
}
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		ns := r.URL.Query().Get("namespace")
		if ns != "system" && ns != "secure" && ns != "global" {
			fail(w, 400, "Invalid namespace")
			return
		}
		out, err := command(10*time.Second, "/system/bin/settings", "list", ns)
		if err != nil {
			fail(w, 500, out)
			return
		}
		rows := []map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok {
				rows = append(rows, map[string]string{"key": k, "value": v})
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i]["key"] < rows[j]["key"] })
		writeJSON(w, 200, rows)
		return
	}
	if r.Method == "PUT" {
		var v struct {
			Namespace string `json:"namespace"`
			Key       string `json:"key"`
			Value     string `json:"value"`
			Delete    bool   `json:"delete"`
		}
		if !decode(w, r, &v) {
			return
		}
		if (v.Namespace != "system" && v.Namespace != "secure" && v.Namespace != "global") || !keyPattern.MatchString(v.Key) || len(v.Value) > 8192 || strings.ContainsRune(v.Value, 0) {
			fail(w, 400, "Invalid setting")
			return
		}
		args := []string{"put", v.Namespace, v.Key, v.Value}
		if v.Delete {
			args = []string{"delete", v.Namespace, v.Key}
		}
		out, err := command(10*time.Second, "/system/bin/settings", args...)
		if err != nil {
			fail(w, 500, out)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	fail(w, 405, "Method not allowed")
}
func (s *Server) handleServices(w http.ResponseWriter, r *http.Request, p []string) {
	if len(p) == 2 && r.Method == "GET" {
		out, err := command(5*time.Second, "/system/bin/getprop")
		if err != nil {
			fail(w, 500, out)
			return
		}
		rows := []map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "[init.svc.") {
				continue
			}
			key, value, ok := strings.Cut(line, "]: [")
			if ok {
				rows = append(rows, map[string]string{"name": strings.TrimPrefix(key, "[init.svc."), "state": strings.TrimSuffix(value, "]")})
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i]["name"] < rows[j]["name"] })
		writeJSON(w, 200, rows)
		return
	}
	if len(p) == 4 && r.Method == "POST" && keyPattern.MatchString(p[2]) && (p[3] == "start" || p[3] == "stop") {
		if p[2] == "sysutils" || p[2] == "adbd" {
			fail(w, 403, "Protected management service")
			return
		}
		out, err := command(5*time.Second, "/system/bin/"+p[3], p[2])
		if err != nil {
			fail(w, 500, out)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	fail(w, 405, "Unsupported action")
}
func main() {
	configureUpdateDNS("/data/local/debian")
	for _, dir := range []string{"/apex/com.android.conscrypt/cacerts", "/system/etc/security/cacerts"} {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
			_ = os.Setenv("SSL_CERT_DIR", dir)
			break
		}
	}
	root := flag.String("root", "/data/local/sysutils", "State directory")
	boot := flag.Bool("boot", false, "Run enabled startup scripts once per boot")
	flag.Parse()
	syscall.Umask(0077)
	s, err := NewServer(*root)
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:9474")
	if err != nil {
		log.Fatal(err)
	}
	if *boot {
		go s.boot()
	}
	log.Print("SysUtils listening on loopback as uid " + strconv.Itoa(os.Getuid()))
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	log.Fatal(server.Serve(listener))
}
