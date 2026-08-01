//go:build e2e

package e2e

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/amakhov/k0smos/internal/control"
	"github.com/amakhov/k0smos/internal/nethub"
)

// Multi-node testing needs guests that can reach each other, which user-mode
// networking cannot do: every guest sits behind its own NAT at the same
// 10.0.2.15 and sees only the host. So each guest gets a second NIC connected to
// an Ethernet hub (internal/nethub) that this test runs, and the cluster talks
// over that.
//
// clusterCIDR addresses that segment. It has no router and no DHCP server, so the
// addresses are static and no gateway is set on it — the default route stays on
// the first NIC, which is also what forwards the API port to the host.
const (
	clusterCIDR   = "10.10.0.%d/24"
	clusterNodeIP = "10.10.0.%d"
	// firstNodeHost is the last octet of node 0. Anything outside the range slirp
	// hands out; these are on a different segment anyway.
	firstNodeHost = 11
)

func nodeIP(i int) string { return fmt.Sprintf(clusterNodeIP, firstNodeHost+i) }

// clusterMAC gives each guest a distinct address on the shared segment. QEMU's
// stock 52:54:00:12:34:56 is the same on every guest, which is invisible behind
// NAT and fatal on a segment they share.
func clusterMAC(i int) string { return fmt.Sprintf("52:54:00:c0:5e:%02x", firstNodeHost+i) }

// clusterSegment starts the Ethernet hub this test's guests share and returns the
// address they connect to. Each test gets its own, so concurrent runs cannot see
// each other's traffic.
//
// It has to be listening before any guest starts: QEMU's connect mode fails at
// startup rather than retrying.
func clusterSegment(t *testing.T) string {
	t.Helper()
	h, err := nethub.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start the cluster segment: %v", err)
	}
	// A dropped port takes a guest off the network permanently, and the guest's
	// own logs only show it as everything becoming unreachable. Fail on it here,
	// where the reason is known.
	h.OnDrop = func(err error) { t.Errorf("cluster segment dropped a guest: %v", err) }
	t.Cleanup(func() { h.Close() })
	return h.Addr()
}

// freePort asks the kernel for an unused TCP port and gives it straight back.
//
// Inherently racy, and fine here: the window is short and the alternative — a
// fixed port — fails outright when two runs overlap or a guest leaks.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free tcp port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// clusterConfig is the k0s configuration for one node of the cluster.
//
// api.address and etcd.peerAddress are per-node and cannot come from the shared
// cluster configuration: they say which address this machine answers on. The SANs
// cover every node's cluster address plus 127.0.0.1, because the test reaches the
// API through a forwarded host port and the certificate has to be valid there too.
func clusterConfig(i, n int) string {
	sans := []string{"127.0.0.1", "localhost"}
	for j := range n {
		sans = append(sans, nodeIP(j))
	}
	return fmt.Sprintf(`apiVersion: k0s.k0sproject.io/v1beta1
kind: ClusterConfig
metadata:
  name: k0s
spec:
  api:
    address: %s
    sans: [%s]
  storage:
    type: etcd
    etcd:
      peerAddress: %s
`, nodeIP(i), strings.Join(sans, ", "), nodeIP(i))
}

// nodeMemMB is how much each guest gets. A control plane that also runs kubelet
// and containerd wants a few gigabytes, and three of them have to fit on the
// machine running the test — a 16GB CI runner cannot spare 6GB each.
// K0SMOS_E2E_CLUSTER_MEM overrides it.
func nodeMemMB() int {
	if v := os.Getenv("K0SMOS_E2E_CLUSTER_MEM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 4096
}

// requireMemoryFor skips unless the machine has room for n guests.
//
// Overcommitting produces an OOM kill partway through a boot, which reads as a
// k0smos failure and is not one. Saying so up front is worth the few lines.
func requireMemoryFor(t *testing.T, n int) {
	t.Helper()
	need := int64(n) * int64(nodeMemMB()) << 20
	total := hostMemory()
	if total == 0 {
		return // cannot tell; let it run rather than skipping for no reason
	}
	// Leave a quarter for the host, docker and the test process itself.
	if avail := total - total/4; avail < need {
		t.Skipf("%d guests at %dMB need %dGB, and this machine has %dGB — "+
			"set K0SMOS_E2E_CLUSTER_MEM lower to run it anyway",
			n, nodeMemMB(), need>>30, total>>30)
	}
}

// hostMemory returns total RAM in bytes, or 0 when it cannot be determined.
func hostMemory() int64 {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return n
	}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if after, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(after), "kB")), 10, 64)
			return kb << 10
		}
	}
	return 0
}

// clusterNode is one guest of the cluster under test.
type clusterNode struct {
	vm      *vm
	name    string
	ip      string
	apiPort int
}

// bootClusterNode starts one node. token is empty for the first one, which
// bootstraps the cluster, and a controller join token for the rest.
//
// Every node runs `k0s controller --enable-worker`: a control plane that also
// carries workloads, which is the topology this tests. --no-taints because
// otherwise nothing would schedule on any of them.
//
// --node-ip is not optional here. Left to itself kubelet picks the address behind
// the default route, which is 10.0.2.15 on every guest — three nodes claiming one
// address, and a cluster that half-works in ways that take a long time to read.
func bootClusterNode(t *testing.T, i, n int, seg, token string) *clusterNode {
	t.Helper()
	name := fmt.Sprintf("k0smos-%d", i)
	ip := nodeIP(i)

	files := fmt.Sprintf(`  - path: /etc/k0s/k0s.yaml
    permissions: "0644"
    content: |
%s`, indent(clusterConfig(i, n), "      "))
	args := []string{
		"/usr/local/bin/k0s", "controller", "--enable-worker", "--no-taints",
		"--config=/etc/k0s/k0s.yaml",
		"--kubelet-extra-args=--node-ip=" + ip,
	}
	if token != "" {
		files += fmt.Sprintf(`  - path: /etc/k0s/join-token
    permissions: "0600"
    content: |
%s`, indent(token, "      "))
		args = append(args, "--token-file=/etc/k0s/join-token")
	}

	iso := makeCidataNamed(t, name, "#cloud-config\nwrite_files:\n"+files,
		fmt.Sprintf("instance-id: i-%s\nlocal-hostname: %s\n", name, name))

	apiPort := freePort(t)
	v := boot(t, bootOpts{
		Name:   name,
		Disk:   cloneDiskAs(t, filepath.Join(repoRoot(t), "dist/k0smos.img"), "-"+name),
		Cidata: iso,
		Data:   seededVolumeNamed(t, name+"-data", 4096),
		// eth0 is slirp, for the forwarded API port. eth1 is the cluster segment.
		Net: fmt.Sprintf("k0smos.ip=eth0:dhcp,eth1:"+clusterCIDR+
			" k0smos.dns=1.1.1.1 k0smos.data=auto", firstNodeHost+i),
		Exec:       strings.Join(args, ","),
		ClusterNet: seg,
		ClusterMAC: clusterMAC(i),
		APIPort:    fmt.Sprint(apiPort),
		Mem:        fmt.Sprint(nodeMemMB()), CPUs: "2",
	})
	return &clusterNode{vm: v, name: name, ip: ip, apiPort: apiPort}
}

// indent prefixes every line, for embedding a document in YAML.
func indent(s, prefix string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString(prefix + line + "\n")
	}
	return b.String()
}

// The second NIC has to exist and be configured before any of the cluster test
// can mean anything, and that part costs one fast boot rather than several
// minutes of k0s. When the cluster test fails, this says whether the problem is
// the segment or the cluster on top of it.
func TestClusterSegmentGivesEachGuestASecondNIC(t *testing.T) {
	requireArtifacts(t, "dist/k0smos-initramfs.gz")
	seg := clusterSegment(t)

	for i := range 2 {
		name := fmt.Sprintf("nic-%d", i)
		v := boot(t, bootOpts{
			Name: name,
			Net: fmt.Sprintf("k0smos.ip=eth0:dhcp,eth1:"+clusterCIDR+" k0smos.dns=1.1.1.1",
				firstNodeHost+i),
			ClusterNet: seg,
			ClusterMAC: clusterMAC(i),
			Exec:       execNoop,
			Mem:        "1024",
		})
		// Both NICs, so a second one that silently displaced the first would fail
		// here rather than as a node with no route to anything.
		v.waitFor(`eth0 configured 10\.0\.2\.\d+/\d+`, bootTimeout)
		v.waitFor(`eth1 configured `+nodeIP(i), bootTimeout)
		v.stop()
	}
}

// A configured interface is not a working one. The multicast backend this
// replaced gave every guest an eth1 that came up, reported an address, and
// carried nothing — which surfaced as "no route to host" twenty minutes into a
// cluster boot, from a node whose networking looked fine.
//
// So: ARP the guest from a port on the same hub and require an answer. That
// exercises both directions — the request has to reach the guest and the reply
// has to come back — and costs one fast boot.
func TestClusterSegmentCarriesTraffic(t *testing.T) {
	requireArtifacts(t, "dist/k0smos-initramfs.gz")
	seg := clusterSegment(t)

	v := boot(t, bootOpts{
		Net: fmt.Sprintf("k0smos.ip=eth1:"+clusterCIDR, firstNodeHost),
		// No eth0 address: this is about the second NIC, and leaving slirp
		// unconfigured means an answer cannot have come back any other way.
		ClusterNet: seg,
		ClusterMAC: clusterMAC(0),
		Exec:       execNoop,
		Mem:        "1024",
	})
	v.waitFor(`eth1 configured `+nodeIP(0), bootTimeout)

	// Another port on the same hub, which is what a second guest would be.
	conn, err := net.DialTimeout("tcp", seg, 5*time.Second)
	if err != nil {
		t.Fatalf("connect to the cluster segment: %v", err)
	}
	defer conn.Close()

	const probeIP = "10.10.0.99"
	probeMAC := mustMAC(t, "52:54:00:c0:5e:99")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := writeQEMUFrame(conn, arpRequest(probeMAC, probeIP, nodeIP(0))); err != nil {
			t.Fatalf("send ARP: %v", err)
		}
		// Several requests: the guest may still be bringing the link up, and a
		// frame sent before it is has nowhere to land.
		if replied, err := awaitARPReply(conn, nodeIP(0), 2*time.Second); err != nil {
			t.Fatalf("read from the segment: %v", err)
		} else if replied {
			v.stop()
			return
		}
	}
	t.Fatalf("%s never answered an ARP on the shared segment, so it carries no traffic",
		nodeIP(0))
}

// A three-node cluster, every node a control plane that also runs workloads.
//
// This is the first test that exercises k0smos as more than one machine: a real
// etcd quorum, nodes joining with a token minted by the first one, and kubelet on
// each of them registering under its own name and address. Everything before it
// could pass with a single guest that never had to talk to anything.
func TestThreeControllerWorkerCluster(t *testing.T) {
	const n = 3
	requireFullSuite(t)
	requireArtifacts(t, "dist/k0smos.img", "dist/k0smos-initramfs.gz")
	requirePristineDisk(t, filepath.Join(repoRoot(t), "dist/k0smos.img"))
	requireMemoryFor(t, n)

	seg := clusterSegment(t)
	t.Logf("cluster segment %s", seg)

	// The first node bootstraps: it creates the CA and the etcd cluster the others
	// join. Nothing else can start until it is serving.
	nodes := []*clusterNode{bootClusterNode(t, 0, n, seg, "")}
	nodes[0].vm.waitFor(`supervising \[/usr/local/bin/k0s controller --enable-worker`, bootTimeout)
	nodes[0].vm.waitFor(`eth1 configured `+nodeIP(0), bootTimeout)
	nodes[0].vm.waitFor(`just became ready`, k0sTimeout)

	// A join token can only be produced by a machine that already holds the
	// cluster CA, which is why this goes through the control port rather than
	// being computed on the host.
	tokenBytes, err := requestNodeWithin(t, nodes[0].vm, control.RequestToken+" controller", 3*time.Minute)
	if err != nil {
		t.Fatalf("mint a controller join token: %v", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if len(token) < 100 {
		t.Fatalf("join token looks wrong (%d bytes): %q", len(token), token)
	}

	// The remaining nodes join with it. Started together: they contact node 0, not
	// each other, so there is nothing to serialise.
	for i := 1; i < n; i++ {
		nodes = append(nodes, bootClusterNode(t, i, n, seg, token))
	}
	for _, node := range nodes[1:] {
		node.vm.waitFor(`eth1 configured `+node.ip, bootTimeout)
	}

	// Reachability over the shared segment, stated as its own failure. A joining
	// node that cannot see node 0 otherwise fails much later and much less clearly.
	for _, node := range nodes[1:] {
		if text := node.vm.waitForAny(k0sTimeout,
			`joined etcd cluster`, `etcd is running`, `just became ready`); text == "" {
			t.Fatalf("%s never joined; the shared segment may not be carrying traffic:\n%s",
				node.name, lastConsoleLines(node.vm.consoleText(), 20))
		}
	}

	// The real assertion: node 0's API lists three nodes, all Ready, each with its
	// own cluster address.
	creds := clusterCreds(t, nodes[0])
	waitForNodes(t, creds.clientFor(t, nodes[0]), nodes[0].apiPort, n, k0sTimeout)

	// Three control planes, not one control plane and two workers: each node
	// answers on its own API server, with a certificate valid for its own address
	// and the same CA as the rest of the cluster.
	for _, node := range nodes {
		if err := checkReadyz(creds.clientFor(t, node), node); err != nil {
			t.Errorf("%s is not serving the API: %v", node.name, err)
		}
	}

	for _, node := range nodes {
		node.vm.stop()
	}
}

// --- raw frames on the shared segment ---

// QEMU's socket backend frames each packet as a 4-byte big-endian length
// followed by the Ethernet frame; internal/nethub speaks the same thing.
func writeQEMUFrame(conn net.Conn, frame []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(frame)))
	_, err := conn.Write(append(hdr[:], frame...))
	return err
}

func mustMAC(t *testing.T, s string) []byte {
	t.Helper()
	hw, err := net.ParseMAC(s)
	if err != nil {
		t.Fatalf("parse MAC %q: %v", s, err)
	}
	return hw
}

// arpRequest builds a broadcast "who has target" for the given sender.
func arpRequest(senderMAC []byte, senderIP, targetIP string) []byte {
	frame := make([]byte, 0, 42)
	frame = append(frame, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff) // broadcast
	frame = append(frame, senderMAC...)
	frame = append(frame, 0x08, 0x06) // ARP
	frame = append(frame,
		0x00, 0x01, // Ethernet
		0x08, 0x00, // IPv4
		6, 4,
		0x00, 0x01) // request
	frame = append(frame, senderMAC...)
	frame = append(frame, net.ParseIP(senderIP).To4()...)
	frame = append(frame, 0, 0, 0, 0, 0, 0) // target MAC, unknown
	return append(frame, net.ParseIP(targetIP).To4()...)
}

// awaitARPReply reports whether a reply announcing fromIP arrives within the
// timeout. A timeout is not an error: the caller retries.
func awaitARPReply(conn net.Conn, fromIP string, timeout time.Duration) (bool, error) {
	want := net.ParseIP(fromIP).To4()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return false, err
	}
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			if isTimeout(err) {
				return false, nil
			}
			return false, err
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n > 1<<16 {
			return false, fmt.Errorf("frame of %d bytes on the segment", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(conn, buf); err != nil {
			if isTimeout(err) {
				return false, nil
			}
			return false, err
		}
		// 14-byte Ethernet header, then ARP: opcode at 6, sender IP at 14.
		if len(buf) >= 42 && buf[12] == 0x08 && buf[13] == 0x06 &&
			buf[14+6] == 0 && buf[14+7] == 2 &&
			string(buf[14+14:14+18]) == string(want) {
			return true, nil
		}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// --- talking to the cluster ---

// creds is the cluster's CA and an admin client certificate, taken from a
// kubeconfig. One set covers every node: they share a CA, so the same credential
// authenticates against all three.
type creds struct {
	pool *x509.CertPool
	pair tls.Certificate
}

// clusterCreds fetches the cluster's credentials from a node's kubeconfig, over
// the control port.
//
// Direct rather than through kubectl or client-go: the credentials are all in the
// kubeconfig, the calls made with them are plain GETs, and neither a binary on the
// host nor a large dependency is worth it.
func clusterCreds(t *testing.T, node *clusterNode) creds {
	t.Helper()
	data, err := requestNode(t, node.vm, control.RequestKubeconfig)
	if err != nil {
		t.Fatalf("fetch kubeconfig from %s: %v", node.name, err)
	}

	var kc struct {
		Clusters []struct {
			Cluster struct {
				CA string `json:"certificate-authority-data"`
			} `json:"cluster"`
		} `json:"clusters"`
		Users []struct {
			User struct {
				Cert string `json:"client-certificate-data"`
				Key  string `json:"client-key-data"`
			} `json:"user"`
		} `json:"users"`
	}
	if err := yaml.Unmarshal(data, &kc); err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	if len(kc.Clusters) == 0 || len(kc.Users) == 0 {
		t.Fatalf("kubeconfig has no cluster or user:\n%s", data)
	}

	ca := decodeB64(t, kc.Clusters[0].Cluster.CA)
	cert := decodeB64(t, kc.Users[0].User.Cert)
	key := decodeB64(t, kc.Users[0].User.Key)

	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatalf("client certificate: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("kubeconfig CA is not a certificate")
	}
	return creds{pool: pool, pair: pair}
}

// clientFor builds an HTTPS client for one node's API server.
//
// Verification is left on. The connection goes to a forwarded port on 127.0.0.1
// while the server's certificate is issued for the node's cluster address, so the
// name is set to the latter — which also checks that each node presents a
// certificate for its own address, signed by the one cluster CA. That is a real
// property of an HA control plane and the SAN list exists to make it hold.
func (c creds) clientFor(t *testing.T, node *clusterNode) *http.Client {
	t.Helper()
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      c.pool,
			Certificates: []tls.Certificate{c.pair},
			ServerName:   node.ip,
		}},
	}
}

func decodeB64(t *testing.T, s string) []byte {
	t.Helper()
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode kubeconfig field: %v", err)
	}
	return out
}

// nodeList is the part of a v1.NodeList this test reads.
type nodeList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Addresses []struct {
				Type    string `json:"type"`
				Address string `json:"address"`
			} `json:"addresses"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// ready reports whether the node at index i has Ready=True.
func (l nodeList) ready(i int) bool {
	for _, c := range l.Items[i].Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

// waitForNodes blocks until the cluster has want nodes, all Ready.
func waitForNodes(t *testing.T, api *http.Client, port, want int, timeout time.Duration) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/api/v1/nodes", port)
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		list, err := getNodes(api, url)
		if err != nil {
			last = err.Error()
			time.Sleep(pollEvery)
			continue
		}
		ready, names := 0, make([]string, 0, len(list.Items))
		for i := range list.Items {
			state := "NotReady"
			if list.ready(i) {
				ready++
				state = "Ready"
			}
			names = append(names, list.Items[i].Metadata.Name+"="+state)
		}
		last = fmt.Sprintf("%d/%d ready: %s", ready, want, strings.Join(names, " "))
		if ready == want && len(list.Items) == want {
			t.Logf("cluster formed: %s", last)
			return
		}
		time.Sleep(pollEvery)
	}
	t.Fatalf("cluster never reached %d ready nodes; last saw %s", want, last)
}

func getNodes(api *http.Client, url string) (nodeList, error) {
	var list nodeList
	resp, err := api.Get(url)
	if err != nil {
		return list, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return list, err
	}
	if resp.StatusCode != http.StatusOK {
		return list, fmt.Errorf("GET nodes: %s: %s", resp.Status, firstBytes(body, 200))
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return list, fmt.Errorf("parse node list: %w", err)
	}
	return list, nil
}

// checkReadyz asks a node's own API server whether it is serving.
//
// Authenticated: k0s does not allow anonymous requests, so an unauthenticated
// /readyz answers 401 rather than saying anything about readiness.
func checkReadyz(c *http.Client, node *clusterNode) error {
	resp, err := c.Get(fmt.Sprintf("https://127.0.0.1:%d/readyz", node.apiPort))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, firstBytes(body, 200))
	}
	return nil
}

func firstBytes(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
