package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A workspace (ADR 0010) is a user-named unit that maps one or more folders to a
// single brain, federated with the global brain — mnemos's analogue of a Claude
// Cowork Project. Unlike the implicit `.mnemos` walk-up (which keys a brain to a
// single folder), a workspace is explicitly created and can span several
// folders; a session's cwd activates whichever workspace owns it (the most
// specific matching folder wins). The explicit name is a portable identity that
// derives a hosted tenant (deriveHostedTenant), so a workspace can be shared.

type workspace struct {
	Folders []string `yaml:"folders"`
	DB      string   `yaml:"db"`
}

type workspaceRegistry struct {
	Workspaces map[string]*workspace `yaml:"workspaces"`
}

// workspaceRegistryPath is the registry file, alongside the config (XDG).
func workspaceRegistryPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".config", "mnemos", "workspaces.yaml")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "mnemos", "workspaces.yaml")
}

func loadWorkspaceRegistry() workspaceRegistry {
	reg := workspaceRegistry{Workspaces: map[string]*workspace{}}
	data, err := os.ReadFile(workspaceRegistryPath()) //nolint:gosec // config-dir path
	if err != nil {
		return reg
	}
	_ = yaml.Unmarshal(data, &reg)
	if reg.Workspaces == nil {
		reg.Workspaces = map[string]*workspace{}
	}
	return reg
}

func saveWorkspaceRegistry(reg workspaceRegistry) error {
	path := workspaceRegistryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := yaml.Marshal(reg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644) //nolint:gosec // non-secret registry
}

// defaultWorkspaceDB is the central brain path for a named workspace (a workspace
// may span several folders, so its brain lives centrally, not in one folder).
func defaultWorkspaceDB(name string) string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "sqlite://" + filepath.Join("data", "workspaces", name+".db")
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return "sqlite://" + filepath.Join(dataHome, "mnemos", "workspaces", name+".db")
}

// resolveWorkspaceBrain returns the DSN, name, and matched folder of the
// registered workspace that owns cwd — the one whose folder is cwd or the
// nearest ancestor of it (most specific path wins). Empty when none matches.
// The hooks/MCP consult this first, then fall back to the .mnemos walk-up.
func resolveWorkspaceBrain(cwd string) (dsn, name, folder string) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", "", ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = filepath.Clean(cwd)
	}
	reg := loadWorkspaceRegistry()
	best := -1
	for wsName, ws := range reg.Workspaces {
		if ws == nil {
			continue
		}
		for _, f := range ws.Folders {
			f = filepath.Clean(f)
			if abs == f || strings.HasPrefix(abs, f+string(os.PathSeparator)) {
				if len(f) > best {
					best = len(f)
					dsn, name, folder = ws.DB, wsName, f
				}
			}
		}
	}
	return dsn, name, folder
}

// handleWorkspace routes `mnemos workspace <create|list|remove>`.
func handleWorkspace(args []string, f Flags) {
	if len(args) == 0 {
		exitWithMnemosError(f.Verbose, NewUserError("workspace requires a subcommand: create, list, remove"))
		return
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		handleWorkspaceCreate(rest, f)
	case "list":
		handleWorkspaceList(rest, f)
	case "remove", "rm":
		handleWorkspaceRemove(rest, f)
	default:
		exitWithMnemosError(f.Verbose, NewUserError("unknown workspace subcommand %q (want create, list, remove)", sub))
	}
}

// handleWorkspaceCreate registers a named workspace over one or more folders.
// The global --db flag (f.DB) sets the brain; otherwise a central per-workspace
// path is used. With no --folder, the current directory is the sole folder.
func handleWorkspaceCreate(args []string, f Flags) {
	name := ""
	var folders []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--folder":
			if i+1 >= len(args) {
				exitWithMnemosError(f.Verbose, NewUserError("--folder requires a directory"))
				return
			}
			folders = append(folders, args[i+1])
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				exitWithMnemosError(f.Verbose, NewUserError("unknown workspace create flag %q", args[i]))
				return
			}
			if name != "" {
				exitWithMnemosError(f.Verbose, NewUserError("workspace create takes one name (got %q and %q)", name, args[i]))
				return
			}
			name = args[i]
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		exitWithMnemosError(f.Verbose, NewUserError("workspace create <name> [--folder <dir>...]"))
		return
	}
	if len(folders) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			exitWithMnemosError(f.Verbose, NewSystemError(err, "resolve current directory"))
			return
		}
		folders = []string{cwd}
	}
	abs := make([]string, 0, len(folders))
	for _, d := range folders {
		a, err := filepath.Abs(d)
		if err != nil {
			exitWithMnemosError(f.Verbose, NewUserError("bad folder %q: %v", d, err))
			return
		}
		abs = append(abs, filepath.Clean(a))
	}
	db := strings.TrimSpace(f.DB)
	if db == "" {
		db = defaultWorkspaceDB(name)
	}

	reg := loadWorkspaceRegistry()
	if _, exists := reg.Workspaces[name]; exists && !f.Force {
		exitWithMnemosError(f.Verbose, NewUserError("workspace %q already exists (use --force to replace)", name))
		return
	}
	reg.Workspaces[name] = &workspace{Folders: abs, DB: db}
	if err := saveWorkspaceRegistry(reg); err != nil {
		exitWithMnemosError(f.Verbose, NewSystemError(err, "save workspace registry"))
		return
	}
	// Bootstrap the brain so the first session/read succeeds.
	if p, ok := sqliteFilePath(db); ok {
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
	}
	fmt.Printf("Created workspace %q\n  folders: %s\n  brain:   %s\n  tenant:  %s\n",
		name, strings.Join(abs, ", "), db, deriveHostedTenant(name))
	fmt.Println("\nSessions started in any of these folders now federate global ∪ this workspace.")
}

func handleWorkspaceList(args []string, f Flags) {
	for _, a := range args {
		exitWithMnemosError(f.Verbose, NewUserError("workspace list takes no flags (got %q)", a))
		return
	}
	reg := loadWorkspaceRegistry()
	names := make([]string, 0, len(reg.Workspaces))
	for n := range reg.Workspaces {
		names = append(names, n)
	}
	sort.Strings(names)

	if f.JSON {
		out := map[string]any{}
		for _, n := range names {
			ws := reg.Workspaces[n]
			out[n] = map[string]any{"folders": ws.Folders, "db": ws.DB, "tenant": deriveHostedTenant(n)}
		}
		emitJSON(out)
		return
	}
	if len(names) == 0 {
		fmt.Println("No workspaces. Create one with: mnemos workspace create <name> --folder <dir>")
		return
	}
	for _, n := range names {
		ws := reg.Workspaces[n]
		fmt.Printf("%s\n  folders: %s\n  brain:   %s\n  tenant:  %s\n", n, strings.Join(ws.Folders, ", "), ws.DB, deriveHostedTenant(n))
	}
}

func handleWorkspaceRemove(args []string, f Flags) {
	if len(args) != 1 {
		exitWithMnemosError(f.Verbose, NewUserError("workspace remove <name>"))
		return
	}
	name := strings.TrimSpace(args[0])
	reg := loadWorkspaceRegistry()
	if _, ok := reg.Workspaces[name]; !ok {
		exitWithMnemosError(f.Verbose, NewUserError("no workspace named %q", name))
		return
	}
	delete(reg.Workspaces, name)
	if err := saveWorkspaceRegistry(reg); err != nil {
		exitWithMnemosError(f.Verbose, NewSystemError(err, "save workspace registry"))
		return
	}
	fmt.Printf("Removed workspace %q (its brain file is left in place).\n", name)
}
