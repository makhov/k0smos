package metadata

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeIMDS serves the endpoints EC2 does. userData empty means an instance
// created without any, which answers 404. v1 drops the token requirement, as an
// instance with IMDSv2 optional does.
func fakeIMDS(t *testing.T, userData string, v1 bool) *httptest.Server {
	t.Helper()
	const token = "AQAEALpL..."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/api/token" {
			if v1 {
				http.Error(w, "not supported", http.StatusNotFound)
				return
			}
			if r.Method != http.MethodPut || r.Header.Get(ttlHeader) == "" {
				http.Error(w, "bad token request", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(token))
			return
		}
		if !v1 && r.Header.Get(tokenHeader) != token {
			// What a real IMDSv2-enforced instance answers an unauthenticated read.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/latest/user-data":
			if userData == "" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(userData))
		case "/latest/meta-data/instance-id":
			_, _ = w.Write([]byte("i-0abc123def456"))
		case "/latest/meta-data/local-hostname":
			_, _ = w.Write([]byte("ip-10-0-3-17.eu-central-1.compute.internal"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The whole point: bootstrap data that arrives over IMDS comes out of Load
// exactly as the same data off a drive would.
func TestLoadFromIMDS(t *testing.T) {
	srv := fakeIMDS(t, "#cloud-config\nwrite_files:\n  - path: /etc/k0s/join-token\n    content: abc123\nruncmd:\n  - [k0s, install, worker, --token-file, /etc/k0s/join-token]\n", false)
	src, err := NewAWS(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ud, md, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(ud.WriteFiles) != 1 || ud.WriteFiles[0].Path != "/etc/k0s/join-token" {
		t.Errorf("write_files = %+v", ud.WriteFiles)
	}
	if md.InstanceID != "i-0abc123def456" {
		t.Errorf("InstanceID = %q", md.InstanceID)
	}
	if md.Hostname != "ip-10-0-3-17.eu-central-1.compute.internal" {
		t.Errorf("Hostname = %q", md.Hostname)
	}
	// Nothing should be reported: network-config has no equivalent here, and a
	// source not carrying a file is not a problem to warn about.
	if len(ud.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", ud.Warnings)
	}
	if got := ud.Plan().Workload; len(got) < 2 || got[1] != "worker" {
		t.Errorf("Workload = %v, want the k0s worker command", got)
	}
}

// An instance with IMDSv2 enforced: every read carries the session token, and
// the fake rejects any that does not.
func TestIMDSv2TokenIsSentOnEveryRead(t *testing.T) {
	srv := fakeIMDS(t, "#cloud-config\n", false)
	src, err := NewAWS(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if src.token == "" {
		t.Fatal("no session token taken")
	}
	if _, err := src.ReadFile("user-data"); err != nil {
		t.Errorf("user-data: %v", err)
	}
	if _, err := src.ReadFile("meta-data"); err != nil {
		t.Errorf("meta-data: %v", err)
	}
}

// An instance that still allows IMDSv1 refuses to issue a token. That is not a
// reason to give up: the reads work without one.
func TestIMDSv1WithoutAToken(t *testing.T) {
	srv := fakeIMDS(t, "#cloud-config\nruncmd:\n  - [k0s, install, controller]\n", true)
	src, err := NewAWS(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if src.token != "" {
		t.Errorf("token = %q, want none", src.token)
	}
	ud, _, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := ud.Plan().Workload; len(got) < 2 || got[1] != "controller" {
		t.Errorf("Workload = %v", got)
	}
}

// An instance created with no user-data answers 404 for it. That is a normal
// machine -- it boots on its cmdline configuration -- so it must read as
// "absent", not as an error, and must not produce a warning.
func TestIMDSWithoutUserDataIsNotAnError(t *testing.T) {
	srv := fakeIMDS(t, "", false)
	src, err := NewAWS(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.ReadFile("user-data"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("user-data error = %v, want fs.ErrNotExist", err)
	}
	ud, md, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(ud.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", ud.Warnings)
	}
	if md.InstanceID == "" {
		t.Error("meta-data should still be read when there is no user-data")
	}
}

// Nothing listening is what every machine off a hyperscaler finds, and it has
// to fail at once rather than once per file.
func TestNewAWSWithNoServiceFails(t *testing.T) {
	srv := fakeIMDS(t, "", false)
	url := srv.URL
	srv.Close()
	if _, err := NewAWS(url); err == nil {
		t.Error("want an error when nothing is listening")
	}
}

// The paths only a drive has must read as absent, so Load does not warn about
// a file this source was never going to carry.
func TestIMDSHasNoDriveOnlyPaths(t *testing.T) {
	srv := fakeIMDS(t, "#cloud-config\n", false)
	src, err := NewAWS(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"network-config", "openstack/latest/user_data", "openstack/latest/meta_data.json"} {
		if _, err := src.ReadFile(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s error = %v, want fs.ErrNotExist", name, err)
		}
	}
}

// A hostname with a character YAML would otherwise read as structure must not
// be able to change the document's shape.
func TestIMDSQuotesMetaDataValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/api/token":
			_, _ = w.Write([]byte("t"))
		case "/latest/meta-data/instance-id":
			_, _ = w.Write([]byte("i-1"))
		case "/latest/meta-data/local-hostname":
			_, _ = w.Write([]byte("host: not-a-key"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	src, err := NewAWS(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := src.ReadFile("meta-data")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"host: not-a-key"`) {
		t.Errorf("meta-data = %q, want the hostname quoted", b)
	}
	md, err := ParseMetaData(b)
	if err != nil {
		t.Fatal(err)
	}
	if md.Hostname != "host: not-a-key" {
		t.Errorf("Hostname = %q", md.Hostname)
	}
}
