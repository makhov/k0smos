package metadata

import (
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"
)

// AWSIMDS is the link-local address an EC2 instance's metadata service answers
// on. The other hyperscalers use the same address with their own paths.
const AWSIMDS = "http://169.254.169.254"

const (
	// imdsTimeout bounds one request. Short, because this runs on the boot path
	// of every machine that has no metadata drive -- bare metal, a smoke test --
	// and those are exactly the machines where nothing will answer.
	imdsTimeout = 3 * time.Second
	// dialTimeout is shorter still: an unreachable link-local address normally
	// fails instantly, and the only case that waits is a route to a host that
	// is not listening.
	dialTimeout = 1 * time.Second
	// maxMetadata caps a reply. EC2 allows 16 KiB of user-data; this is well
	// over that, and exists so a service answering with something enormous
	// cannot exhaust PID1's memory.
	maxMetadata = 1 << 20

	tokenHeader = "X-aws-ec2-metadata-token"
	ttlHeader   = "X-aws-ec2-metadata-token-ttl-seconds"
)

// AWS reads cloud-init data from EC2's instance metadata service.
//
// An EC2 instance has no drive to attach a seed to, so this is the only way a
// node there can be told which cluster to join. It satisfies Files by mapping
// cloud-init's canonical names onto the service's endpoints, which leaves Load,
// Apply, Plan and everything under them unchanged.
//
// shortcut: AWS only. GCP and Azure serve the same data at their own paths
// behind their own headers -- each is another Files beside this one, not a
// change to it.
type AWS struct {
	base   string
	client *http.Client
	token  string
}

// NewAWS prepares a reader for the service at base, or AWSIMDS when base is
// empty. It returns an error when nothing is listening there, which is the
// normal case off-cloud: the caller then pays one short timeout rather than one
// per file read.
func NewAWS(base string) (*AWS, error) {
	if base == "" {
		base = AWSIMDS
	}
	a := &AWS{
		base: base,
		client: &http.Client{
			Timeout: imdsTimeout,
			// No proxy, and no redirect followed: this is a link-local address,
			// and a proxy from the environment would send a node's bootstrap
			// credentials somewhere else entirely.
			Transport: &http.Transport{
				Proxy:       nil,
				DialContext: (&net.Dialer{Timeout: dialTimeout}).DialContext,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	// IMDSv2 wants a session token on every read. Reaching the service at all
	// is the point of this request; an instance that still allows v1 answers it
	// with an error status, and is then read without a token.
	req, err := http.NewRequest(http.MethodPut, a.base+"/latest/api/token", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(ttlHeader, "21600")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("no metadata service at %s: %w", a.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata))
		if err != nil {
			return nil, fmt.Errorf("read session token: %w", err)
		}
		a.token = strings.TrimSpace(string(b))
	}
	return a, nil
}

// ReadFile serves the cloud-init names this service has an equivalent for.
func (a *AWS) ReadFile(name string) ([]byte, error) {
	switch name {
	case "user-data":
		return a.get("/latest/user-data")
	case "meta-data":
		return a.metaData()
	}
	// The config-drive layout and network-config have no equivalent here.
	// fs.ErrNotExist is what Load reads as "this source does not carry that",
	// and is the one error it does not warn about.
	return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
}

// metaData builds the document ParseMetaData expects out of the values the
// service serves one per path: it has a tree of endpoints, not a file. A value
// that is missing is left out rather than failing -- an instance always has an
// id, but its hostname can be turned off.
func (a *AWS) metaData() ([]byte, error) {
	var b strings.Builder
	for _, f := range []struct{ field, path string }{
		{"instance-id", "/latest/meta-data/instance-id"},
		{"local-hostname", "/latest/meta-data/local-hostname"},
	} {
		v, err := a.get(f.path)
		if err != nil {
			continue
		}
		// Quoted, so a value with a colon or a leading character YAML treats
		// specially cannot change the document's shape.
		fmt.Fprintf(&b, "%s: %q\n", f.field, strings.TrimSpace(string(v)))
	}
	if b.Len() == 0 {
		return nil, fmt.Errorf("meta-data: %w", fs.ErrNotExist)
	}
	return []byte(b.String()), nil
}

func (a *AWS) get(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, a.base+path, nil)
	if err != nil {
		return nil, err
	}
	if a.token != "" {
		req.Header.Set(tokenHeader, a.token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()
	// An instance with no user-data at all answers 404, which is a normal
	// machine and not a failure to report.
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", path, resp.Status)
	}
	// One byte over the cap, so a reply at the limit is told apart from one
	// past it: silently truncating a join token would be far worse than failing.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(b) > maxMetadata {
		return nil, fmt.Errorf("%s: over the %d-byte limit for metadata", path, maxMetadata)
	}
	return b, nil
}
