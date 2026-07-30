package metadata

import "path"

// serviceManagers are commands that only make sense with a service manager.
// k0smos supervises one child directly, so these are dropped rather than run.
var serviceManagers = map[string]bool{
	"systemctl":  true,
	"service":    true,
	"rc-update":  true,
	"rc-service": true,
}

// Workload separates the long-running child from one-shot setup commands.
//
// CAPI bootstrap providers are written for a systemd machine: they run
// `k0s install <role> [args]` to write a unit file and then `k0s start`. k0smos
// has no service manager and supervises a single child, so the install form is
// translated into the equivalent foreground command (`k0s <role> [args]`) and
// service-manager calls are discarded.
//
// argv is nil when the user-data described no workload, which tells the caller
// to keep its own default.
func (u UserData) Workload() (argv []string, oneshots [][]string) {
	for _, cmd := range u.RunCmd {
		if len(cmd) == 0 {
			continue
		}
		bin := path.Base(cmd[0])

		// `k0s install <role> ...` -> supervise `k0s <role> ...`
		if bin == "k0s" && len(cmd) >= 3 && cmd[1] == "install" {
			argv = append([]string{cmd[0]}, cmd[2:]...)
			continue
		}
		// `k0s start`/`stop` manage the unit that was never written.
		if bin == "k0s" && len(cmd) >= 2 && (cmd[1] == "start" || cmd[1] == "stop") {
			continue
		}
		if serviceManagers[bin] {
			continue
		}
		oneshots = append(oneshots, cmd)
	}
	return argv, oneshots
}
