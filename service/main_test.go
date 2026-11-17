package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T) *Server {
	t.Helper()
	s, e := NewServer(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func request(s *Server, method, path, body string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:9474"+path, strings.NewReader(body))
	if auth {
		r.Header.Set("Authorization", "Bearer "+s.token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func create(t *testing.T, s *Server, body string) Script {
	t.Helper()
	v := Script{Name: "Test", Body: body, Timeout: 5, Order: 10}
	b, _ := json.Marshal(v)
	w := request(s, "POST", "/v1/scripts", string(b), true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &v)
	return v
}
func TestAuthenticationAndOrigin(t *testing.T) {
	s := setup(t)
	if w := request(s, "GET", "/v1/scripts", "", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:9474/v1/scripts", nil)
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set("Origin", "https://example.org")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r.Header.Del("Origin")
	r.Host = "attacker.example"
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestCRUDPersistenceAndPaths(t *testing.T) {
	s := setup(t)
	v := create(t, s, "echo hello\n")
	next, e := NewServer(s.root)
	if e != nil || next.scripts[v.ID].Body != v.Body {
		t.Fatal(e)
	}
	v.Body = "echo changed\n"
	b, _ := json.Marshal(v)
	if w := request(s, "PUT", "/v1/scripts/"+v.ID, string(b), true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := request(s, "GET", "/v1/scripts/../../token", "", true); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request(s, "DELETE", "/v1/scripts/"+v.ID, "", true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if len(s.list()) != 0 {
		t.Fatal("not deleted")
	}
}
func TestScriptRunAndExit(t *testing.T) {
	s := setup(t)
	v := create(t, s, "echo hello; exit 7\n")
	done, e := s.run(v.ID)
	if e != nil {
		t.Fatal(e)
	}
	<-done
	rows := s.list()
	if rows[0].Result.Exit != 7 || rows[0].Result.Running {
		t.Fatal(rows)
	}
	w := request(s, "GET", "/v1/scripts/"+v.ID+"/logs", "", true)
	if !strings.Contains(w.Body.String(), "hello") {
		t.Fatal(w.Body)
	}
}
func TestTimeoutAndRunningMutation(t *testing.T) {
	s := setup(t)
	v := create(t, s, "while :; do :; done\n")
	v.Timeout = 1
	s.scripts[v.ID] = v
	done, e := s.run(v.ID)
	if e != nil {
		t.Fatal(e)
	}
	if w := request(s, "DELETE", "/v1/scripts/"+v.ID, "", true); w.Code != 409 {
		t.Fatal(w.Code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout failed")
	}
	if s.list()[0].Result.Error != "context deadline exceeded" {
		t.Fatal(s.list())
	}
}
func TestBootOnceAndDisabled(t *testing.T) {
	s := setup(t)
	s.bootID = "test-boot"
	v := create(t, s, "echo run >> boot-test\n")
	v.Enabled = true
	s.scripts[v.ID] = v
	create(t, s, "echo disabled >> boot-test\n")
	s.boot()
	s.boot()
	b, e := os.ReadFile(filepath.Join(s.root, "boot-test"))
	if e != nil || string(b) != "run\n" {
		t.Fatal(string(b), e)
	}
}
func TestValidationAndBodyLimit(t *testing.T) {
	s := setup(t)
	for _, b := range []string{`{}`, `{"name":"x","body":"echo hi","timeout":0}`, `{"unexpected":1}`, `{} {}`, string(bytes.Repeat([]byte("x"), 300*1024))} {
		if w := request(s, "POST", "/v1/scripts", b, true); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}
func TestBoundedLogs(t *testing.T) {
	w := &tailWriter{}
	w.Write(bytes.Repeat([]byte("a"), 200*1024))
	if len(w.bytes()) != 128*1024 {
		t.Fatal(len(w.bytes()))
	}
}
func TestManagementProtection(t *testing.T) {
	s := setup(t)
	for _, name := range []string{"sysutils", "adbd"} {
		if w := request(s, "POST", "/v1/services/"+name+"/stop", "{}", true); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}
