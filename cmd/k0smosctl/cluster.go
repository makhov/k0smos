package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/amakhov/k0smos/internal/control"
	"github.com/amakhov/k0smos/internal/iso9660"
	"github.com/amakhov/k0smos/internal/metadata"
	"github.com/amakhov/k0smos/internal/nethub"
)

const (
	clusterFirstHost = 11
	clusterSubnet    = "10.10.0"
)

type clusterCreateOptions struct {
	name, image, firmware, arch string
	release, cacheDir           string
	controllers, workers        int
	apiPort, memory, cpus       int
	kubeconfig                  string
	timeout                     time.Duration
	dryRun                      bool
}

type clusterMachine struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	IP      string `json:"ip"`
	APIPort int    `json:"apiPort,omitempty"`
}

type clusterMeta struct {
	Name       string           `json:"name"`
	HubPID     int              `json:"hubPid"`
	HubAddress string           `json:"hubAddress"`
	Machines   []clusterMachine `json:"machines"`
	Created    time.Time        `json:"created"`
}

func clusterCreateCmd() *cobra.Command {
	o := clusterCreateOptions{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a local multi-machine k0s cluster from one artifact",
		Long: `Creates a local k0s cluster from one firmware-bootable k0smos qcow2.

When --image is omitted, the matching qcow2 is downloaded from the requested
GitHub release, checksum-verified, and reused from the local cache. Every machine
receives its own copy-on-write clone and config drive, but boots that same
immutable artifact. A rootless userspace Ethernet segment connects the machines;
the first controller bootstraps k0s, and join tokens minted by that controller
are placed on the remaining machines' config drives.

Controllers also run workloads, so the default one-controller cluster is useful
without a worker. The command waits for the API and writes an immediately usable
kubeconfig before returning.`,
		Example: `  # smallest useful cluster, from the latest GitHub release
  k0smosctl cluster create --name dev

  # test a locally built artifact
  k0smosctl cluster create --name dev --image dist/k0smos-metal-x86_64.qcow2

  # highly available control plane plus workers
  k0smosctl cluster create --name dev --controllers 3 --workers 2`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("image") && (cmd.Flags().Changed("release") || cmd.Flags().Changed("cache-dir")) {
				return errors.New("--image bypasses release resolution, so it cannot be combined with --release or --cache-dir")
			}
			return createCluster(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.name, "name", "dev", "cluster name; machine names are derived from it")
	f.StringVar(&o.image, "image", "", "firmware-bootable qcow2; bypasses GitHub release resolution")
	f.StringVar(&o.release, "release", "latest", "k0s-tagged GitHub release to use when --image is omitted: latest or vX.Y.Z+k0s.N")
	f.StringVar(&o.cacheDir, "cache-dir", "", "release artifact cache (default ~/.cache/k0smos/images)")
	f.StringVar(&o.firmware, "firmware", "", "UEFI code image (auto-detected from QEMU when omitted)")
	f.StringVar(&o.arch, "arch", runtime.GOARCH, "guest architecture: amd64 or arm64")
	f.IntVar(&o.controllers, "controllers", 1, "number of controller machines")
	f.IntVar(&o.workers, "workers", 0, "number of worker-only machines")
	f.IntVar(&o.apiPort, "api-port", 6443, "host port for the first controller; later controllers use consecutive ports")
	f.IntVar(&o.memory, "memory", 4096, "memory per machine in MiB")
	f.IntVar(&o.cpus, "cpus", 2, "CPUs per machine")
	f.StringVarP(&o.kubeconfig, "output", "o", "kubeconfig", "where to write the admin kubeconfig")
	f.DurationVar(&o.timeout, "timeout", 10*time.Minute, "time allowed for the cluster to become ready")
	f.BoolVar(&o.dryRun, "dry-run", false, "print the machine plan without starting anything")
	return cmd
}

func clusterRemoveCmd() *cobra.Command {
	var name string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "rm",
		Aliases: []string{"delete"},
		Short:   "Shut down and discard a local cluster",
		Long: `Shuts every machine down cleanly, stops the cluster's userspace network,
then removes the machine clones, config drives and recorded cluster state.

It refuses to remove disks if a machine does not shut down within the timeout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := clusterStateDir(name)
			if err != nil {
				return err
			}
			var state clusterMeta
			body, err := os.ReadFile(filepath.Join(dir, "cluster.json"))
			if os.IsNotExist(err) {
				return fmt.Errorf("no cluster named %q", name)
			}
			if err != nil {
				return err
			}
			if err := json.Unmarshal(body, &state); err != nil {
				return err
			}

			for _, machine := range state.Machines {
				_, socket, metaPath, err := guestPaths(machine.Name)
				if err != nil {
					return err
				}
				meta, metaErr := loadMeta(metaPath)
				if metaErr == nil && processRunning(meta.PID) {
					conn, err := dial(socket, 5*time.Second)
					if err != nil {
						return fmt.Errorf("machine %q is running but does not accept a clean shutdown: %w", machine.Name, err)
					}
					_, err = fmt.Fprintf(conn, "%s\n", control.PowerOff.String())
					conn.Close()
					if err != nil {
						return err
					}
				}
			}

			deadline := time.Now().Add(timeout)
			for _, machine := range state.Machines {
				_, _, metaPath, _ := guestPaths(machine.Name)
				meta, err := loadMeta(metaPath)
				if err != nil {
					continue
				}
				for processRunning(meta.PID) && time.Now().Before(deadline) {
					time.Sleep(250 * time.Millisecond)
				}
				if processRunning(meta.PID) {
					return fmt.Errorf("machine %q did not stop within %s; no disks were removed", machine.Name, timeout)
				}
			}

			if state.HubPID > 0 {
				_ = syscall.Kill(state.HubPID, syscall.SIGTERM)
			}
			for _, machine := range state.Machines {
				machineDir, err := guestDir(machine.Name)
				if err != nil {
					return err
				}
				if err := os.RemoveAll(machineDir); err != nil {
					return err
				}
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed cluster %q and %d machine(s)\n", name, len(state.Machines))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "dev", "cluster to remove")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "time allowed for clean machine shutdown")
	return cmd
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func createCluster(cmd *cobra.Command, o clusterCreateOptions) error {
	machines, err := planCluster(o)
	if err != nil {
		return err
	}
	if o.dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "cluster %q: %d controller(s), %d worker(s)\n", o.name, o.controllers, o.workers)
		for _, m := range machines {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-10s %-28s %s", m.Role, m.Name, m.IP)
			if m.APIPort != 0 {
				fmt.Fprintf(cmd.OutOrStdout(), " API :%d", m.APIPort)
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
		return nil
	}
	if o.image == "" {
		g, err := guestFor(o.arch)
		if err != nil {
			return err
		}
		o.image, err = resolveReleaseArtifact(cmd.Context(), g.apkArch, o.release, o.cacheDir, cmd.ErrOrStderr())
		if err != nil {
			return err
		}
	}

	dir, err := clusterStateDir(o.name)
	if err != nil {
		return err
	}
	metaPath := filepath.Join(dir, "cluster.json")
	if _, err := os.Stat(metaPath); err == nil {
		return fmt.Errorf("cluster %q already exists in %s", o.name, dir)
	} else if !os.IsNotExist(err) {
		return err
	}
	// Cluster creation is declarative, so it must never silently adopt disks from
	// an older set of individually-created machines. Those disks can contain a
	// different CA, node UID and etcd member identity even when QEMU is stopped.
	for _, machine := range machines {
		machineDir, err := guestDir(machine.Name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(machineDir); err == nil {
			return fmt.Errorf("machine %q already exists; remove it with `k0smosctl machine rm --name %s` before creating this cluster", machine.Name, machine.Name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	hubAddr, hubPID, err := startClusterHub(dir)
	if err != nil {
		return err
	}
	state := clusterMeta{Name: o.name, HubPID: hubPID, HubAddress: hubAddr, Machines: machines, Created: time.Now()}
	if err := writeJSON(metaPath, state); err != nil {
		_ = syscall.Kill(hubPID, syscall.SIGTERM)
		_ = os.RemoveAll(dir)
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "creating cluster %q from one artifact\n", o.name)
	fmt.Fprintf(cmd.OutOrStdout(), "  network: %s (hub pid %d)\n", hubAddr, hubPID)

	deadline := time.Now().Add(o.timeout)
	first := machines[0]
	if err := writeClusterDrive(dir, first, machines, ""); err != nil {
		cleanupUnstartedCluster(dir, hubPID, first.Name)
		return err
	}
	if err := startClusterMachine(cmd, o, first, hubAddr, 0, dir); err != nil {
		cleanupUnstartedCluster(dir, hubPID, first.Name)
		return err
	}

	for i, m := range machines[1:] {
		token, err := awaitToken(first.Name, m.Role, deadline)
		if err != nil {
			return partialClusterError(o.name, fmt.Errorf("mint %s token for %s: %w", m.Role, m.Name, err))
		}
		if err := writeClusterDrive(dir, m, machines, string(token)); err != nil {
			return partialClusterError(o.name, err)
		}
		if err := startClusterMachine(cmd, o, m, hubAddr, i+1, dir); err != nil {
			return partialClusterError(o.name, err)
		}
	}

	data, err := awaitKubeconfig(first.Name, deadline)
	if err != nil {
		return partialClusterError(o.name, err)
	}
	data, err = rewriteServer(data, fmt.Sprintf("127.0.0.1:%d", first.APIPort))
	if err != nil {
		return partialClusterError(o.name, err)
	}
	if err := awaitNodesReady(data, len(machines), deadline); err != nil {
		return partialClusterError(o.name, err)
	}
	if err := os.WriteFile(o.kubeconfig, data, 0600); err != nil {
		return partialClusterError(o.name, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "cluster %q is ready\n", o.name)
	fmt.Fprintf(cmd.OutOrStdout(), "  kubeconfig: %s\n", o.kubeconfig)
	fmt.Fprintf(cmd.OutOrStdout(), "  use it:     KUBECONFIG=%s kubectl get nodes\n", o.kubeconfig)
	return nil
}

func cleanupUnstartedCluster(clusterDir string, hubPID int, machineName string) {
	_ = syscall.Kill(hubPID, syscall.SIGTERM)
	if dir, err := guestDir(machineName); err == nil {
		_ = os.RemoveAll(dir)
	}
	_ = os.RemoveAll(clusterDir)
}

func planCluster(o clusterCreateOptions) ([]clusterMachine, error) {
	if err := validGuestName(o.name); err != nil {
		return nil, fmt.Errorf("invalid cluster name: %w", err)
	}
	if len(o.name) > 40 || !clusterNamePattern.MatchString(o.name) {
		return nil, errors.New("--name must be a lowercase DNS label of at most 40 characters")
	}
	if o.controllers < 1 {
		return nil, errors.New("--controllers must be at least 1")
	}
	if o.workers < 0 {
		return nil, errors.New("--workers cannot be negative")
	}
	if o.controllers+o.workers > 200 {
		return nil, errors.New("a cluster may contain at most 200 machines")
	}
	if o.apiPort < 1 || o.apiPort+o.controllers-1 > 65535 {
		return nil, errors.New("--api-port and the controller count must fit in the TCP port range")
	}
	if o.memory < 256 || o.cpus < 1 {
		return nil, errors.New("each machine needs at least 256 MiB and one CPU")
	}
	if o.timeout <= 0 {
		return nil, errors.New("--timeout must be positive")
	}
	if o.kubeconfig == "" {
		return nil, errors.New("--output cannot be empty")
	}

	var out []clusterMachine
	n := 0
	for i := range o.controllers {
		m := clusterMachine{
			Name: fmt.Sprintf("%s-controller-%d", o.name, i), Role: "controller",
			IP: clusterNodeIP(n), APIPort: o.apiPort + i,
		}
		if _, socket, _, err := guestPaths(m.Name); err != nil {
			return nil, err
		} else if err := checkSocketPath(socket); err != nil {
			return nil, err
		}
		out = append(out, m)
		n++
	}
	for i := range o.workers {
		m := clusterMachine{Name: fmt.Sprintf("%s-worker-%d", o.name, i), Role: "worker", IP: clusterNodeIP(n)}
		if _, socket, _, err := guestPaths(m.Name); err != nil {
			return nil, err
		} else if err := checkSocketPath(socket); err != nil {
			return nil, err
		}
		out = append(out, m)
		n++
	}
	return out, nil
}

var clusterNamePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

func clusterNodeIP(i int) string  { return fmt.Sprintf("%s.%d", clusterSubnet, clusterFirstHost+i) }
func clusterNodeMAC(i int) string { return fmt.Sprintf("52:54:00:c0:5e:%02x", clusterFirstHost+i) }

func clusterStateDir(name string) (string, error) {
	if err := validGuestName(name); err != nil {
		return "", fmt.Errorf("invalid cluster name: %w", err)
	}
	root, err := stateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".clusters", name), nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}

func partialClusterError(name string, err error) error {
	return fmt.Errorf("cluster %q was only partially created: %w; inspect it with `k0smosctl machine list` or remove it with `k0smosctl cluster rm --name %s`", name, err, name)
}

func startClusterMachine(cmd *cobra.Command, o clusterCreateOptions, m clusterMachine, hub string, index int, dir string) error {
	args := []string{
		"--name", m.Name,
		"--cidata", filepath.Join(dir, m.Name+".iso"),
		"--api-port", fmt.Sprint(m.APIPort),
		"--memory", fmt.Sprint(o.memory),
		"--cpus", fmt.Sprint(o.cpus),
		"--arch", o.arch,
		"--cluster-network", hub,
		"--cluster-mac", clusterNodeMAC(index),
	}
	if o.image != "" {
		args = append(args, "--image", o.image)
	}
	if o.firmware != "" {
		args = append(args, "--firmware", o.firmware)
	}
	up := machineUpCmd()
	up.SetArgs(args)
	up.SetIn(cmd.InOrStdin())
	up.SetOut(cmd.OutOrStdout())
	up.SetErr(cmd.ErrOrStderr())
	up.SilenceErrors = true
	up.SilenceUsage = true
	return up.ExecuteContext(cmd.Context())
}

type clusterCloudConfig struct {
	K0smos struct {
		IP  string `json:"ip"`
		DNS string `json:"dns"`
	} `json:"k0smos"`
	WriteFiles []clusterWriteFile `json:"write_files"`
	RunCmd     [][]string         `json:"runcmd"`
}

type clusterWriteFile struct {
	Path        string `json:"path"`
	Permissions string `json:"permissions"`
	Encoding    string `json:"encoding"`
	Content     string `json:"content"`
}

func writeClusterDrive(dir string, machine clusterMachine, all []clusterMachine, token string) error {
	var cc clusterCloudConfig
	cc.K0smos.IP = "eth0:dhcp,eth1:" + machine.IP + "/24"
	cc.K0smos.DNS = guestDNS

	args := []string{"/usr/local/bin/k0s", "install", machine.Role}
	if machine.Role == "controller" {
		cfg := renderK0sConfig(machine, all)
		cc.WriteFiles = append(cc.WriteFiles, encodedFile("/etc/k0s/k0s.yaml", "0644", cfg))
		args = append(args, "--enable-worker", "--no-taints", "--config=/etc/k0s/k0s.yaml")
	}
	args = append(args, "--kubelet-extra-args=--node-ip="+machine.IP)
	if token != "" {
		cc.WriteFiles = append(cc.WriteFiles, encodedFile("/etc/k0s/join-token", "0600", token))
		args = append(args, "--token-file=/etc/k0s/join-token")
	}
	cc.RunCmd = [][]string{args}
	body, err := yaml.Marshal(cc)
	if err != nil {
		return err
	}
	body = append([]byte("#cloud-config\n"), body...)

	meta := []byte(fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", machine.Name, machine.Name))
	if _, _, err := metadata.Load(inMemory{"user-data": body, "meta-data": meta}); err != nil {
		return fmt.Errorf("validate %s config drive: %w", machine.Name, err)
	}
	f, err := os.Create(filepath.Join(dir, machine.Name+".iso"))
	if err != nil {
		return err
	}
	defer f.Close()
	return iso9660.Write(f, metadata.NoCloudLabel, []iso9660.File{
		{Name: "user-data", Data: body}, {Name: "meta-data", Data: meta},
	})
}

func encodedFile(path, mode, content string) clusterWriteFile {
	return clusterWriteFile{Path: path, Permissions: mode, Encoding: "b64", Content: base64.StdEncoding.EncodeToString([]byte(content))}
}

func renderK0sConfig(machine clusterMachine, all []clusterMachine) string {
	sans := []string{"127.0.0.1", "localhost"}
	for _, m := range all {
		sans = append(sans, m.IP)
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
`, machine.IP, strings.Join(sans, ", "), machine.IP)
}

func awaitToken(name, role string, deadline time.Time) ([]byte, error) {
	socket, err := resolveSocket("", name)
	if err != nil {
		return nil, err
	}
	return retryRequest(socket, control.RequestToken+" "+role, deadline)
}

func awaitKubeconfig(name string, deadline time.Time) ([]byte, error) {
	socket, err := resolveSocket("", name)
	if err != nil {
		return nil, err
	}
	data, err := retryRequest(socket, control.RequestKubeconfig, deadline)
	if err != nil {
		return nil, fmt.Errorf("wait for the Kubernetes API: %w", err)
	}
	return data, nil
}

func retryRequest(socket, message string, deadline time.Time) ([]byte, error) {
	var last error
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		attempt := 3 * time.Minute
		if remaining < attempt {
			attempt = remaining
		}
		data, err := request(socket, message, attempt)
		if err == nil {
			return data, nil
		}
		last = err
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("timed out: %w", last)
}

type kubeconfigCredentials struct {
	Clusters []struct {
		Cluster struct {
			Server string `json:"server"`
			CA     string `json:"certificate-authority-data"`
		} `json:"cluster"`
	} `json:"clusters"`
	Users []struct {
		User struct {
			Certificate string `json:"client-certificate-data"`
			Key         string `json:"client-key-data"`
		} `json:"user"`
	} `json:"users"`
}

func awaitNodesReady(kubeconfig []byte, want int, deadline time.Time) error {
	client, server, err := kubeClient(kubeconfig)
	if err != nil {
		return err
	}
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(server, "/")+"/api/v1/nodes", nil)
		if err == nil {
			var resp *http.Response
			resp, err = client.Do(req)
			if err == nil {
				var list struct {
					Items []struct {
						Status struct {
							Conditions []struct {
								Type, Status string
							} `json:"conditions"`
						} `json:"status"`
					} `json:"items"`
				}
				if resp.StatusCode != http.StatusOK {
					err = fmt.Errorf("Kubernetes API returned %s", resp.Status)
				} else if decodeErr := json.NewDecoder(resp.Body).Decode(&list); decodeErr != nil {
					err = decodeErr
				} else {
					ready := 0
					for _, node := range list.Items {
						for _, condition := range node.Status.Conditions {
							if condition.Type == "Ready" && condition.Status == "True" {
								ready++
							}
						}
					}
					if len(list.Items) == want && ready == want {
						resp.Body.Close()
						cancel()
						return nil
					}
					err = fmt.Errorf("Kubernetes reports %d/%d nodes, %d Ready", len(list.Items), want, ready)
				}
				resp.Body.Close()
			}
		}
		cancel()
		last = err
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("wait for all nodes to become Ready: %w", last)
}

func kubeClient(data []byte) (*http.Client, string, error) {
	var cfg kubeconfigCredentials
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, "", fmt.Errorf("parse kubeconfig: %w", err)
	}
	if len(cfg.Clusters) == 0 || len(cfg.Users) == 0 {
		return nil, "", errors.New("kubeconfig has no cluster or user")
	}
	ca, err := base64.StdEncoding.DecodeString(cfg.Clusters[0].Cluster.CA)
	if err != nil {
		return nil, "", fmt.Errorf("decode kubeconfig CA: %w", err)
	}
	certPEM, err := base64.StdEncoding.DecodeString(cfg.Users[0].User.Certificate)
	if err != nil {
		return nil, "", fmt.Errorf("decode kubeconfig client certificate: %w", err)
	}
	keyPEM, err := base64.StdEncoding.DecodeString(cfg.Users[0].User.Key)
	if err != nil {
		return nil, "", fmt.Errorf("decode kubeconfig client key: %w", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig client certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, "", errors.New("kubeconfig contains no usable CA certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: []tls.Certificate{cert},
	}}
	return &http.Client{Transport: transport}, cfg.Clusters[0].Cluster.Server, nil
}

// clusterHubCmd is an implementation detail of cluster create. It is a separate
// process because QEMU's socket network needs the hub for the cluster's whole
// lifetime, while create must return once Kubernetes is ready.
func clusterHubCmd() *cobra.Command {
	var listen, ready string
	cmd := &cobra.Command{
		Use:    "__hub",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := nethub.Listen(listen)
			if err != nil {
				return err
			}
			defer h.Close()
			h.OnDrop = func(err error) { fmt.Fprintf(cmd.ErrOrStderr(), "cluster network: %v\n", err) }
			if err := os.WriteFile(ready, []byte(h.Addr()+"\n"), 0600); err != nil {
				return err
			}
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sig)
			<-sig
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "listen address")
	cmd.Flags().StringVar(&ready, "ready-file", "", "write the selected address here")
	_ = cmd.MarkFlagRequired("ready-file")
	return cmd
}

func startClusterHub(dir string) (string, int, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", 0, err
	}
	ready := filepath.Join(dir, "hub.ready")
	_ = os.Remove(ready)
	logPath := filepath.Join(dir, "hub.log")
	log, err := os.Create(logPath)
	if err != nil {
		return "", 0, err
	}
	defer log.Close()
	child := exec.Command(exe, "__hub", "--ready-file", ready)
	child.Stdout, child.Stderr, child.Stdin = log, log, nil
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return "", 0, err
	}
	pid := child.Process.Pid
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(ready)
		if err == nil {
			addr := strings.TrimSpace(string(b))
			if _, _, err := net.SplitHostPort(addr); err == nil {
				if err := child.Process.Release(); err != nil {
					_ = child.Process.Kill()
					return "", 0, err
				}
				return addr, pid, nil
			}
		}
		if err := syscall.Kill(pid, 0); err != nil {
			body, _ := os.ReadFile(logPath)
			return "", 0, fmt.Errorf("cluster network hub exited: %s", strings.TrimSpace(string(body)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = child.Process.Kill()
	return "", 0, errors.New("cluster network hub did not become ready")
}
