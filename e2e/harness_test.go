//go:build e2e

// Package e2e boots k0smos under QEMU and asserts on what actually happens.
//
// These tests exist because every interesting bug in this project was found by
// booting, not by unit tests: the cold-boot mount ordering, kubelet's refusal to
// run on a ramfs root, an empty PID1 PATH, missing netfilter modules, a closed
// channel read as a shutdown request. Unit tests could not have caught any of
// them, and each was found by hand — repeatedly, which is what this replaces.
//
// Run with:
//
//	make e2e        # fast: no k0s, ~40s per boot
//	make e2e-full   # adds the k0s tests, several minutes each
package e2e

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/amakhov/k0smos/internal/iso9660"
	"github.com/amakhov/k0smos/internal/metadata"
)

const (
	// bootTimeout bounds a boot that never reaches its marker. The fast tests never
	// start k0s and reach their markers in 10-25s locally and 16-24s on CI, so a
	// minute is already four times the observed worst case. It was three minutes,
	// which meant one wrong assumption cost 18 tests x 3 minutes and blew a CI job's
	// whole timeout.
	bootTimeout = time.Minute
	// k0sTimeout bounds the slow tests. Generous on purpose: the first boot pulls
	// every image over QEMU's user-mode network, so convergence has been observed
	// anywhere between 5 and 13 minutes on the same machine. A tight bound here
	// produces a failure that looks like a k0smos bug and is not one.
	k0sTimeout = 25 * time.Minute
	pollEvery  = 2 * time.Second
)

// repoRoot is the directory holding the Makefile, resolved once.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// requireArtifacts skips rather than fails when the images have not been built:
// a missing artifact is a setup problem, not a k0smos defect.
func requireArtifacts(t *testing.T, names ...string) {
	t.Helper()
	root := repoRoot(t)
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(root, n)); err != nil {
			t.Skipf("missing %s — run 'make kernel initramfs disk' first", n)
		}
	}
}

// requirePristineDisk refuses to run a k0s test against a root image that
// already holds cluster state.
//
// Booting a pre-bootstrapped image is silently useless: its PKI and kubelet.conf
// belong to whatever node name built them, so a node with a new name never
// registers and the test times out looking like a k0smos failure. This cost a
// 25-minute run to diagnose, hence the guard.
func requirePristineDisk(t *testing.T, img string) {
	t.Helper()
	abs, err := filepath.Abs(img)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "run", "--rm",
		"-v", filepath.Dir(abs)+":/d", "alpine:3.20", "sh", "-c",
		"apk add -q --no-cache e2fsprogs e2fsprogs-extra >/dev/null && "+
			"debugfs -R 'ls /var/lib/k0s' /d/"+filepath.Base(abs)+" 2>/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return // cannot tell; let the test proceed and fail on its own terms
	}
	for _, stale := range []string{"pki", "kubelet.conf", "db"} {
		if strings.Contains(string(out), stale) {
			t.Skipf("%s already holds k0s state (%q) — rebuild it with 'make disk' "+
				"before running the k0s tests", img, stale)
		}
	}
}

// vm is a running guest.
type vm struct {
	t    *testing.T
	root string
	// name distinguishes guests within one test, so several of them do not save
	// their consoles over each other. Empty for a test that boots only one.
	name    string
	console string
	control string
	cmd     *exec.Cmd
	stderr  *bytes.Buffer
	done    chan struct{}
}

// bootOpts configures a boot. Zero values mean "the script's default".
type bootOpts struct {
	// Disk is the root image. Empty stays on the initramfs.
	Disk string
	// Initramfs overrides the default initramfs.
	Initramfs string
	// Cidata is a cloud-init ISO to attach.
	Cidata string
	// Exec sets k0smos.exec. Use execNoop for a workload that exits at once,
	// which keeps a boot to ~40s by never starting k0s.
	Exec string
	// Net replaces the default networking cmdline fragment.
	Net string
	// Data is a mutable data volume; created blank if absent.
	Data string
	// Mem and CPUs size the guest.
	Mem, CPUs string
	// Extra is appended to the kernel cmdline via NET_ARGS.
	Root string
	// ClusterNet is the host:port of an Ethernet hub (internal/nethub), giving the
	// guest a second NIC on a segment shared with every other guest connected to
	// it. ClusterMAC must then be unique per guest.
	ClusterNet, ClusterMAC string
	// APIPort forwards a host port to the guest's 6443.
	APIPort string
	// Name distinguishes this guest from others in the same test. Needed only
	// when a test boots more than one, so their consoles are told apart.
	Name string
}

// execNoop is k0smos itself: as a child it fails the PID1 gate and exits 1, so
// the supervisor has something real to run without paying for k0s.
const execNoop = "/sbin/k0smos"

func boot(t *testing.T, o bootOpts) *vm {
	t.Helper()
	root := repoRoot(t)
	dir := t.TempDir()

	// The control socket must live on a short path: macOS caps an AF_UNIX path
	// at ~104 bytes, and t.TempDir() under /var/folders already spends most of
	// that, so QEMU silently fails to bind.
	sockDir, err := os.MkdirTemp("/tmp", "k0e")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })

	v := &vm{
		t:       t,
		root:    root,
		name:    o.Name,
		console: filepath.Join(dir, "console.log"),
		control: filepath.Join(sockDir, "c.sock"),
		done:    make(chan struct{}),
	}

	env := append(os.Environ(),
		"SERIAL="+v.console,
		"CONTROL="+v.control,
		"MEM="+orDefault(o.Mem, "2048"),
		"CPUS="+orDefault(o.CPUs, "2"),
	)
	for k, val := range map[string]string{
		"IMG":         o.Disk,
		"INITRAMFS":   o.Initramfs,
		"CIDATA":      o.Cidata,
		"EXEC":        o.Exec,
		"DATA":        o.Data,
		"NET_ARGS":    o.Net,
		"ROOT":        o.Root,
		"CLUSTER_NET": o.ClusterNet,
		"CLUSTER_MAC": o.ClusterMAC,
		"API_PORT":    o.APIPort,
	} {
		if val != "" {
			env = append(env, k+"="+val)
		}
	}

	v.cmd = exec.Command("bash", filepath.Join(root, "image", "run-qemu.sh"))
	v.cmd.Dir = root
	v.cmd.Env = env
	// Capture the runner's own output: when QEMU refuses to start, its complaint
	// goes here and nowhere near the guest console.
	v.stderr = &bytes.Buffer{}
	v.cmd.Stdout = v.stderr
	v.cmd.Stderr = v.stderr
	if err := v.cmd.Start(); err != nil {
		t.Fatalf("start qemu: %v", err)
	}
	go func() { v.cmd.Wait(); close(v.done) }()

	// Always tear down, even on failure, and always via the control port: a
	// hard kill corrupts the ext4 image and makes any later disk assertion lie.
	t.Cleanup(func() {
		v.stop()
		// Consoles are kept on failure because that is when they are evidence.
		// K0SMOS_E2E_KEEP_CONSOLE=1 keeps them regardless, for checking what a
		// passing boot actually printed — a conditional assertion can pass by
		// being skipped, and the console is the only way to tell.
		if t.Failed() || os.Getenv("K0SMOS_E2E_KEEP_CONSOLE") == "1" {
			v.dumpConsole()
		}
	})
	return v
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// waitFor blocks until the console matches pattern, and fails the test if QEMU
// exits or the deadline passes.
//
// An idle-console check was tried here and removed: it never fired on the case that
// motivated it. A guest whose assertion is wrong is not quiet — the supervisor
// restarts the exiting workload forever, so the console scrolls continuously while
// the awaited line never comes. Bounding a broken run is done with a shorter
// bootTimeout and -failfast in CI instead, which are blunt but actually work.
func (v *vm) waitFor(pattern string, timeout time.Duration) string {
	v.t.Helper()
	re := regexp.MustCompile(pattern)
	deadline := time.Now().Add(timeout)
	for {
		text := v.consoleText()
		if m := re.FindString(text); m != "" {
			return m
		}
		select {
		case <-v.done:
			// One last look: the match may have arrived with the final flush.
			if m := re.FindString(v.consoleText()); m != "" {
				return m
			}
			v.t.Fatalf("qemu exited before console matched %q\nrunner output:\n%s",
				pattern, v.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			v.t.Fatalf("timed out after %s waiting for %q\nlast lines:\n%s",
				timeout, pattern, lastConsoleLines(text, 15))
		}
		time.Sleep(pollEvery)
	}
}

// waitForAny blocks until any of the patterns matches, and returns the match, or
// "" if none did before the timeout.
//
// Unlike waitFor it does not fail the test: it is for a step that can be reached
// several ways, where the caller has a better failure to report than "none of
// these strings appeared".
func (v *vm) waitForAny(timeout time.Duration, patterns ...string) string {
	v.t.Helper()
	res := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		res[i] = regexp.MustCompile(p)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		text := v.consoleText()
		for _, re := range res {
			if m := re.FindString(text); m != "" {
				return m
			}
		}
		select {
		case <-v.done:
			return "" // the guest is gone; nothing more will be printed
		default:
		}
		time.Sleep(pollEvery)
	}
	return ""
}

// lastConsoleLines returns the tail of the console, so a failure says what the
// guest was doing rather than only what it failed to say.
func lastConsoleLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// refute fails if pattern ever appears before the guest reaches settled.
func (v *vm) refute(pattern, settled string, timeout time.Duration) {
	v.t.Helper()
	v.waitFor(settled, timeout)
	if re := regexp.MustCompile(pattern); re.MatchString(v.consoleText()) {
		v.t.Errorf("console contains %q, which it must not:\n  %s",
			pattern, re.FindString(v.consoleText()))
	}
}

// ansi strips terminal escapes, which the kernel and k0s both emit.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func (v *vm) consoleText() string {
	b, err := os.ReadFile(v.console)
	if err != nil {
		return ""
	}
	return ansi.ReplaceAllString(string(b), "")
}

// stop asks the guest to power off cleanly and waits for QEMU to exit.
func (v *vm) stop() {
	select {
	case <-v.done:
		return // already gone
	default:
	}
	if _, err := os.Stat(v.control); err == nil {
		cmd := exec.Command("bash", filepath.Join(v.root, "image", "poweroff.sh"), v.control)
		cmd.Dir = v.root
		cmd.Run()
	}
	select {
	case <-v.done:
	case <-time.After(90 * time.Second):
		v.t.Errorf("guest did not power off within 90s; killing (image may be dirty)")
		if v.cmd.Process != nil {
			v.cmd.Process.Kill()
		}
		<-v.done
	}
}

func (v *vm) dumpConsole() {
	// Keep the full console somewhere durable: t.TempDir() is deleted when the
	// test ends, which threw away the evidence for the first failure this suite
	// produced.
	stem := sanitise(v.t.Name())
	if v.name != "" {
		stem += "-" + v.name
	}
	if saved := filepath.Join(v.root, "dist", "e2e", stem+".console.log"); true {
		if err := os.MkdirAll(filepath.Dir(saved), 0755); err == nil {
			if b, err := os.ReadFile(v.console); err == nil {
				if os.WriteFile(saved, b, 0644) == nil {
					v.t.Logf("full console saved to %s", saved)
				}
			}
		}
	}
	text := v.consoleText()
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > 60 {
		lines = lines[len(lines)-60:]
	}
	v.t.Logf("--- last %d console lines (%s) ---\n%s",
		len(lines), v.console, strings.Join(lines, "\n"))
}

// k0smosLines returns just the init's own output, which is what most assertions
// care about.
func (v *vm) k0smosLines() string {
	var out []string
	for _, l := range strings.Split(v.consoleText(), "\n") {
		if strings.Contains(l, "k0smos:") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// --- cloud-init drives ---

// makeCidata builds a NoCloud ISO, the way a CAPI infrastructure provider would
// attach one. xorriso runs in a container so the test needs no host tooling.
func makeCidata(t *testing.T, userData, metaData string) string {
	// -J -r matches what KubeVirt produces (xorrisofs -joliet -rock).
	return makeCidataOpts(t, "", userData, metaData, "-J", "-r")
}

// makeCidataNamed is makeCidata for a test that builds more than one drive, as a
// cluster does: one per node, each with its own hostname and configuration.
//
// Written with the ISO writer this repository ships rather than xorriso in a
// container. It needs no Docker, which a test that already boots three VMs has
// enough dependencies without, and it is the drive `k0smosctl gen` produces — so
// the cluster test follows the path the documentation tells a user to follow.
// Compatibility with what a real provider emits is xorriso's job, and iso_test.go
// covers it against drives built both ways.
func makeCidataNamed(t *testing.T, name, userData, metaData string) string {
	t.Helper()
	outDir := filepath.Join(repoRoot(t), "dist", "e2e")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(outDir, sanitise(t.Name())+"-"+name+".iso")
	t.Cleanup(func() { os.Remove(iso) })

	f, err := os.Create(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if metaData == "" {
		metaData = "instance-id: e2e\n"
	}
	if err := iso9660.Write(f, metadata.NoCloudLabel, []iso9660.File{
		{Name: "user-data", Data: []byte(userData)},
		{Name: "meta-data", Data: []byte(metaData)},
	}); err != nil {
		t.Fatalf("write cidata iso: %v", err)
	}
	return iso
}

// makeCidataOpts builds the drive with explicit xorriso extension flags, so a
// test can pin which ISO9660 extensions k0smos actually depends on.
func makeCidataOpts(t *testing.T, name, userData, metaData string, isoFlags ...string) string {
	t.Helper()
	root := repoRoot(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "user-data"), []byte(userData), 0644); err != nil {
		t.Fatal(err)
	}
	if metaData == "" {
		metaData = "instance-id: e2e\n"
	}
	if err := os.WriteFile(filepath.Join(src, "meta-data"), []byte(metaData), 0644); err != nil {
		t.Fatal(err)
	}

	// The ISO must live under the repo: run-qemu.sh resolves paths from there.
	outDir := filepath.Join(root, "dist", "e2e")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	stem := sanitise(t.Name())
	if name != "" {
		stem += "-" + name
	}
	iso := filepath.Join(outDir, stem+".iso")
	t.Cleanup(func() { os.Remove(iso) })

	cmd := exec.Command("docker", "run", "--rm",
		"-v", src+":/in", "-v", outDir+":/out",
		"alpine:3.20", "sh", "-c",
		"apk add -q --no-cache xorriso >/dev/null && xorriso -as mkisofs -V cidata "+
			strings.Join(isoFlags, " ")+" -o /out/"+filepath.Base(iso)+" /in 2>/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build cidata iso: %v\n%s", err, out)
	}
	return iso
}

// gzipBase64 encodes content the way a provider ships a large manifest.
func gzipBase64(t *testing.T, content string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// makeConfigDrive builds an OpenStack-style config-drive: label "config-2" and
// the openstack/latest/ layout.
//
// This is what Cluster API actually produces on KubeVirt — CAPK attaches
// bootstrap data as CloudInitConfigDrive, not NoCloud — so it is the path that
// matters most, despite NoCloud being easier to hand-write.
func makeConfigDrive(t *testing.T, userData, metaDataJSON string) string {
	t.Helper()
	root := repoRoot(t)
	src := t.TempDir()
	sub := filepath.Join(src, "openstack", "latest")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "user_data"), []byte(userData), 0644); err != nil {
		t.Fatal(err)
	}
	if metaDataJSON == "" {
		metaDataJSON = `{"uuid":"e2e-cd"}`
	}
	if err := os.WriteFile(filepath.Join(sub, "meta_data.json"), []byte(metaDataJSON), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(root, "dist", "e2e")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(outDir, sanitise(t.Name())+"-cd.iso")
	t.Cleanup(func() { os.Remove(iso) })

	cmd := exec.Command("docker", "run", "--rm",
		"-v", src+":/in", "-v", outDir+":/out",
		"alpine:3.20", "sh", "-c",
		"apk add -q --no-cache xorriso >/dev/null && xorriso -as mkisofs -V config-2 -J -r -o /out/"+
			filepath.Base(iso)+" /in 2>/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build config-drive iso: %v\n%s", err, out)
	}
	return iso
}

func sanitise(s string) string {
	return strings.NewReplacer("/", "_", " ", "_").Replace(s)
}

// --- disk assertions ---

// blankVolume returns a path for a data volume that does not exist yet, so
// run-qemu.sh creates it blank. It is deliberately not removed between the two
// boots of the reuse test.
func blankVolume(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(repoRoot(t), "dist", "e2e", sanitise(t.Name())+"-"+name+".img")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	os.Remove(p)
	t.Cleanup(func() { os.Remove(p) })
	return p
}

// cloneDisk copies the root image so a test can assert on guest writes without
// disturbing other tests. The image is sparse, so this is cheaper than its
// apparent size suggests.
func cloneDisk(t *testing.T, src string) string {
	return cloneDiskAs(t, src, "")
}

// cloneDisk2 is a second, independent clone for a test that boots twice.
func cloneDisk2(t *testing.T, src string) string {
	return cloneDiskAs(t, src, "-2")
}

func cloneDiskAs(t *testing.T, src, suffix string) string {
	t.Helper()
	dst := filepath.Join(repoRoot(t), "dist", "e2e", sanitise(t.Name())+suffix+".img")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(dst) })
	// -c clones on APFS, which is instant; fall back to a plain copy elsewhere.
	if err := exec.Command("cp", "-c", src, dst).Run(); err != nil {
		if out, err := exec.Command("cp", src, dst).CombinedOutput(); err != nil {
			t.Fatalf("clone disk: %v\n%s", err, out)
		}
	}
	return dst
}

// debugfsCmd runs a debugfs command against an image. This is how container logs
// and guest-written files are inspected: no mount, no privileges, and the reason
// a clean shutdown matters — a dirty image reads as empty directories.
func debugfsCmd(t *testing.T, img, command string) string {
	t.Helper()
	abs, err := filepath.Abs(img)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "run", "--rm",
		"-v", filepath.Dir(abs)+":/d", "alpine:3.20", "sh", "-c",
		"apk add -q --no-cache e2fsprogs e2fsprogs-extra >/dev/null && "+
			fmt.Sprintf("debugfs -R %q /d/%s 2>/dev/null", command, filepath.Base(abs)))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("debugfs %q: %v", command, err)
	}
	return string(out)
}

// requireFsckClean asserts a read-only check finds nothing, which only holds if
// shutdown remounted the root read-only and checkpointed the journal.
func requireFsckClean(t *testing.T, img string) {
	t.Helper()
	abs, _ := filepath.Abs(img)
	cmd := exec.Command("docker", "run", "--rm",
		"-v", filepath.Dir(abs)+":/d", "alpine:3.20", "sh", "-c",
		"apk add -q --no-cache e2fsprogs >/dev/null && e2fsck -fn /d/"+filepath.Base(abs)+" 2>&1")
	out, _ := cmd.CombinedOutput()
	text := string(out)
	for _, bad := range []string{
		"still has errors",
		"skipping journal recovery", // journal left unreplayed => remount-ro failed
		"Free blocks count wrong",
		"Free inodes count wrong",
	} {
		if strings.Contains(text, bad) {
			t.Errorf("filesystem not clean after shutdown (%q):\n%s", bad, text)
			return
		}
	}
}

// seededVolume builds a data volume that already contains k0s's airgap image
// bundle, and skips the test when the bundle has not been fetched.
//
// This is the difference between a k0s boot taking a couple of minutes and taking
// five to thirteen: without it the first boot pulls every image over QEMU's
// user-mode network, which is the only reason k0sTimeout is 25 minutes.
//
// The volume is formatted here rather than by k0smos, and labelled so k0smos mounts
// it as-is: formatting is exactly what would erase the bundle. It is mounted at
// /var, so the bundle belongs at lib/k0s/images/ within it — <data-dir>/images is
// where k0s looks, and k0s's data directory is /var/lib/k0s.
func seededVolume(t *testing.T, sizeMB int) string {
	return seededVolumeNamed(t, "seeded", sizeMB)
}

// seededVolumeNamed is seededVolume for a test that needs more than one, as a
// cluster does: every node wants its own copy of the bundle.
//
// The formatted image is built once and kept in dist, then cloned per volume.
// Building it costs a container, a 350MB copy and an mkfs; three nodes paying
// that each, on every run, came to more than the boots they were speeding up.
func seededVolumeNamed(t *testing.T, name string, sizeMB int) string {
	t.Helper()
	src := airgapSeed(t, sizeMB)
	dst := filepath.Join(repoRoot(t), "dist", "e2e", sanitise(t.Name())+"-"+name+".img")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	os.Remove(dst)
	t.Cleanup(func() { os.Remove(dst) })
	// -c clones on APFS, which is instant; fall back to a plain copy elsewhere.
	if err := exec.Command("cp", "-c", src, dst).Run(); err != nil {
		if out, err := exec.Command("cp", src, dst).CombinedOutput(); err != nil {
			t.Fatalf("clone seeded volume: %v\n%s", err, out)
		}
	}
	return dst
}

// airgapSeed returns a formatted data volume holding the airgap bundle, building
// it if it is missing or older than the bundle. Shared by every test and kept
// between runs; the per-test copies are clones of it.
func airgapSeed(t *testing.T, sizeMB int) string {
	t.Helper()
	root := repoRoot(t)
	dist := filepath.Join(root, "dist")
	bundle := filepath.Join(dist, "k0s-airgap-"+runtime.GOARCH+".tar")
	bundleInfo, err := os.Stat(bundle)
	if err != nil {
		t.Skipf("missing %s — run './image/fetch-airgap.sh' to make the k0s boots fast", bundle)
	}

	seed := filepath.Join(dist, fmt.Sprintf("k0s-airgap-%s-seed-%dM.img", runtime.GOARCH, sizeMB))
	// Rebuilt when the bundle is newer, so fetching a new k0s version is enough
	// to invalidate it. Not cleaned up: it is a cache, and rebuilding costs a
	// container plus a 350MB copy.
	if info, err := os.Stat(seed); err == nil && info.ModTime().After(bundleInfo.ModTime()) {
		return seed
	}
	os.Remove(seed)

	// Paths inside the container, relative to the bind-mounted dist. Getting this
	// wrong is silent: run-qemu.sh creates a blank volume for a DATA path that does
	// not exist, so the guest boots, formats it, and pulls every image anyway.
	inBundle := "/d/" + filepath.Base(bundle)
	inImg := "/d/" + filepath.Base(seed)

	// -d takes a directory tree; -L is what stops k0smos treating it as blank.
	script := fmt.Sprintf(`apk add -q --no-cache e2fsprogs >/dev/null
mkdir -p /w/lib/k0s/images
cp %s /w/lib/k0s/images/bundle.tar
truncate -s %dM %s
mkfs.ext4 -q -L %s -d /w %s`,
		inBundle, sizeMB, inImg, dataVolumeLabel, inImg)

	cmd := exec.Command("docker", "run", "--rm",
		"-v", dist+":/d", "alpine:3.20", "sh", "-c", "mkdir -p /w && "+script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build seeded data volume: %v\n%s", err, out)
	}
	// The whole point is that QEMU gets a volume with the bundle in it. A missing
	// or blank file here means the guest silently falls back to pulling.
	info, err := os.Stat(seed)
	if err != nil {
		t.Fatalf("seeded volume was not written to %s: %v", seed, err)
	}
	if info.Size() < int64(sizeMB)<<20 {
		t.Fatalf("seeded volume %s is %dMB, want %dMB", seed, info.Size()>>20, sizeMB)
	}
	return seed
}

// dataVolumeLabel must match config.Config's DataLabel default, which is what
// k0smos looks for before deciding a device is blank and formatting it.
const dataVolumeLabel = "k0smos-data"
