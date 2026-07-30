package metadata

import (
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// ActionKind is a file operation interpreted from runcmd.
type ActionKind int

const (
	Mkdir ActionKind = iota + 1
	Chmod
	Chown
	Symlink
)

// Action is one interpreted runcmd entry. Nothing here is executed as a
// process: each kind maps to a syscall k0smos makes itself.
type Action struct {
	Kind   ActionKind
	Path   string
	Mode   fs.FileMode // Chmod
	UID    int         // Chown
	GID    int         // Chown
	Target string      // Symlink
}

// Plan is what user-data asked for, after interpretation.
type Plan struct {
	// Workload is the long-running child to supervise, or nil if user-data did
	// not describe one.
	Workload []string
	// Actions are file operations to perform, in order.
	Actions []Action
	// Unsupported holds commands that were recognised as commands but not
	// interpretable. They are skipped, never executed.
	Unsupported [][]string
}

// serviceManagers only make sense with a service manager. k0smos supervises one
// child directly, so these are dropped rather than reported as a problem.
var serviceManagers = map[string]bool{
	"systemctl":  true,
	"service":    true,
	"rc-update":  true,
	"rc-service": true,
}

// namedOwners is the whole user database the image ships (see mkrootfs.sh).
// Anything else cannot be resolved without a passwd lookup, so it is refused
// rather than guessed.
var namedOwners = map[string]int{"root": 0, "nobody": 65534}

// Plan interprets runcmd. k0smos never executes a binary named in user-data:
// machine state stays a function of its configuration, and the image needs
// neither a shell nor coreutils. Recognised forms become typed actions or the
// supervised workload; everything else is reported as unsupported.
func (u UserData) Plan() Plan {
	var p Plan
	for _, cmd := range u.RunCmd {
		if len(cmd) == 0 {
			continue
		}
		bin := path.Base(cmd[0])

		switch {
		// `k0s install <role> ...` describes the workload; supervising it is
		// what `k0s start` would have achieved.
		case bin == "k0s" && len(cmd) >= 3 && cmd[1] == "install":
			p.Workload = append([]string{cmd[0]}, cmd[2:]...)
			continue
		case bin == "k0s" && len(cmd) >= 2 && (cmd[1] == "start" || cmd[1] == "stop"):
			continue
		case serviceManagers[bin]:
			continue
		}

		actions, ok := interpret(bin, cmd)
		if !ok {
			p.Unsupported = append(p.Unsupported, cmd)
			continue
		}
		p.Actions = append(p.Actions, actions...)
	}
	return p
}

// interpret maps one file-manipulation command to actions, reporting false when
// it cannot be understood exactly.
func interpret(bin string, cmd []string) ([]Action, bool) {
	args := cmd[1:]
	switch bin {
	case "mkdir":
		paths := flags(args)
		if len(paths) == 0 {
			return nil, false
		}
		// -p is implied: creating a parent is never wrong here, and refusing
		// the non-p form would reject common bootstrap data for no benefit.
		out := make([]Action, 0, len(paths))
		for _, p := range paths {
			out = append(out, Action{Kind: Mkdir, Path: p})
		}
		return out, true

	case "chmod":
		rest := flags(args)
		if len(rest) < 2 {
			return nil, false
		}
		mode, err := strconv.ParseUint(rest[0], 8, 32)
		if err != nil {
			return nil, false // symbolic modes such as u+x are not interpreted
		}
		out := make([]Action, 0, len(rest)-1)
		for _, p := range rest[1:] {
			out = append(out, Action{Kind: Chmod, Path: p, Mode: fs.FileMode(mode)})
		}
		return out, true

	case "chown":
		rest := flags(args)
		if len(rest) < 2 {
			return nil, false
		}
		uid, gid, ok := owner(rest[0])
		if !ok {
			return nil, false
		}
		out := make([]Action, 0, len(rest)-1)
		for _, p := range rest[1:] {
			out = append(out, Action{Kind: Chown, Path: p, UID: uid, GID: gid})
		}
		return out, true

	case "ln":
		rest := flags(args)
		// Only symbolic links: a hard link needs semantics we do not model.
		if !hasFlag(args, "-s") || len(rest) != 2 {
			return nil, false
		}
		return []Action{{Kind: Symlink, Target: rest[0], Path: rest[1]}}, true
	}
	return nil, false
}

// flags strips leading dash arguments, returning the operands.
func flags(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func hasFlag(args []string, want string) bool {
	for _, a := range args {
		if a == want || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, want[1:])) {
			return true
		}
	}
	return false
}

// owner parses user[:group], accepting numeric ids and the few names the image
// actually defines.
func owner(spec string) (uid, gid int, ok bool) {
	u, g, found := strings.Cut(spec, ":")
	if !found {
		g = u
	}
	uid, ok = resolveOwner(u)
	if !ok {
		return 0, 0, false
	}
	gid, ok = resolveOwner(g)
	if !ok {
		return 0, 0, false
	}
	return uid, gid, true
}

func resolveOwner(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	id, ok := namedOwners[s]
	return id, ok
}

// Applier performs the interpreted actions. internal/sys satisfies it.
type Applier interface {
	MkdirAll(path string, perm fs.FileMode) error
	Chmod(path string, mode fs.FileMode) error
	Chown(path string, uid, gid int) error
	Symlink(target, link string) error
}

// RunActions performs each action, returning every failure rather than stopping
// at the first: partial setup with a reported problem beats abandoning the boot.
func RunActions(a Applier, actions []Action) []error {
	var errs []error
	for _, act := range actions {
		var err error
		switch act.Kind {
		case Mkdir:
			err = a.MkdirAll(act.Path, 0755)
		case Chmod:
			err = a.Chmod(act.Path, act.Mode)
		case Chown:
			err = a.Chown(act.Path, act.UID, act.GID)
		case Symlink:
			err = a.Symlink(act.Target, act.Path)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", act.Path, err))
		}
	}
	return errs
}
