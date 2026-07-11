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
	// Active is an explicit pin (`workspace use <name>`): when set to a known
	// workspace, it overrides folder-based resolution so every session federates
	// that workspace regardless of cwd. Empty = folder-based activation.
	Active string `yaml:"active,omitempty"`
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
	reg := loadWorkspaceRegistry()
	// An explicit pin (`workspace use`) overrides folder resolution: the session
	// uses this workspace regardless of cwd. Its first folder is the AGENTS.md root.
	if p := strings.TrimSpace(reg.Active); p != "" {
		if ws := reg.Workspaces[p]; ws != nil {
			root := ""
			if len(ws.Folders) > 0 {
				root = ws.Folders[0]
			}
			return ws.DB, p, root
		}
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", "", ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = filepath.Clean(cwd)
	}
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
	case "use":
		handleWorkspaceUse(rest, f)
	case "export":
		handleWorkspaceExport(rest, f)
	case "import":
		handleWorkspaceImport(rest, f)
	default:
		exitWithMnemosError(f.Verbose, NewUserError("unknown workspace subcommand %q (want create, list, remove, use, export, import)", sub))
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
			out[n] = map[string]any{"folders": ws.Folders, "db": ws.DB, "tenant": deriveHostedTenant(n), "active": n == reg.Active}
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
		marker := ""
		if n == reg.Active {
			marker = "  ● pinned (workspace use)"
		}
		fmt.Printf("%s%s\n  folders: %s\n  brain:   %s\n  tenant:  %s\n", n, marker, strings.Join(ws.Folders, ", "), ws.DB, deriveHostedTenant(n))
	}
}

// handleWorkspaceUse pins/unpins the active workspace (ADR 0010 follow-up). A pin
// overrides folder-based activation so a session federates the named workspace
// regardless of cwd — useful when working from a folder outside its folder list.
// `use` with no arg prints the current pin; `use --none` (or --clear) removes it.
func handleWorkspaceUse(args []string, f Flags) {
	reg := loadWorkspaceRegistry()
	if len(args) == 0 {
		if strings.TrimSpace(reg.Active) == "" {
			fmt.Println("No workspace pinned (activation is by folder). Pin one with: mnemos workspace use <name>")
			return
		}
		fmt.Printf("Pinned workspace: %s\n", reg.Active)
		return
	}
	if len(args) > 1 {
		exitWithMnemosError(f.Verbose, NewUserError("workspace use takes a single name (or --none)"))
		return
	}
	if args[0] == "--none" || args[0] == "--clear" {
		reg.Active = ""
		if err := saveWorkspaceRegistry(reg); err != nil {
			exitWithMnemosError(f.Verbose, NewSystemError(err, "save workspace registry"))
			return
		}
		fmt.Println("Unpinned. Workspace activation is back to folder-based.")
		return
	}
	name := strings.TrimSpace(args[0])
	if strings.HasPrefix(name, "-") {
		exitWithMnemosError(f.Verbose, NewUserError("unknown workspace use flag %q", name))
		return
	}
	if _, ok := reg.Workspaces[name]; !ok {
		exitWithMnemosError(f.Verbose, NewUserError("no workspace named %q (see: mnemos workspace list)", name))
		return
	}
	reg.Active = name
	if err := saveWorkspaceRegistry(reg); err != nil {
		exitWithMnemosError(f.Verbose, NewSystemError(err, "save workspace registry"))
		return
	}
	fmt.Printf("Pinned workspace %q. Every session federates global ∪ %s until: mnemos workspace use --none\n", name, name)
}

// workspaceExport is the shareable definition of a workspace (`workspace export`):
// enough for another machine to recreate it. The name is the portable identity —
// it derives the same hosted tenant everywhere — so teammates sharing a hosted
// brain only need matching names; folders/db are machine-specific hints the
// importer can override.
type workspaceExport struct {
	Name    string   `yaml:"name"`
	Folders []string `yaml:"folders"`
	DB      string   `yaml:"db"`
	Tenant  string   `yaml:"tenant"` // informational; derived from Name
}

// handleWorkspaceExport emits a workspace's definition as YAML — to --out <file>
// (commit it to a repo to share) or stdout.
func handleWorkspaceExport(args []string, f Flags) {
	name, out := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out":
			if i+1 >= len(args) {
				exitWithMnemosError(f.Verbose, NewUserError("--out requires a file path"))
				return
			}
			out = args[i+1]
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				exitWithMnemosError(f.Verbose, NewUserError("unknown workspace export flag %q", args[i]))
				return
			}
			if name != "" {
				exitWithMnemosError(f.Verbose, NewUserError("workspace export takes one name (got %q and %q)", name, args[i]))
				return
			}
			name = args[i]
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		exitWithMnemosError(f.Verbose, NewUserError("workspace export <name> [--out <file>]"))
		return
	}
	reg := loadWorkspaceRegistry()
	ws := reg.Workspaces[name]
	if ws == nil {
		exitWithMnemosError(f.Verbose, NewUserError("no workspace named %q", name))
		return
	}
	data, err := yaml.Marshal(workspaceExport{Name: name, Folders: ws.Folders, DB: ws.DB, Tenant: deriveHostedTenant(name)})
	if err != nil {
		exitWithMnemosError(f.Verbose, NewSystemError(err, "marshal workspace"))
		return
	}
	if out == "" {
		fmt.Print(string(data))
		return
	}
	if err := os.WriteFile(out, data, 0o644); err != nil { //nolint:gosec // non-secret definition
		exitWithMnemosError(f.Verbose, NewSystemError(err, "write %s", out))
		return
	}
	fmt.Printf("Exported workspace %q → %s\n  Share it (e.g. commit to the repo); recreate with: mnemos workspace import %s\n", name, out, out)
}

// handleWorkspaceImport recreates a workspace from an exported definition. Folders
// and db are machine-specific, so --folder (repeatable) and the global --db
// override the file's values; the name (hence the hosted tenant) is preserved so
// the imported workspace shares the source's tenant.
func handleWorkspaceImport(args []string, f Flags) {
	file := ""
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
				exitWithMnemosError(f.Verbose, NewUserError("unknown workspace import flag %q", args[i]))
				return
			}
			if file != "" {
				exitWithMnemosError(f.Verbose, NewUserError("workspace import takes one file (got %q and %q)", file, args[i]))
				return
			}
			file = args[i]
		}
	}
	if strings.TrimSpace(file) == "" {
		exitWithMnemosError(f.Verbose, NewUserError("workspace import <file> [--folder <dir>...]"))
		return
	}
	data, err := os.ReadFile(file) //nolint:gosec // user-supplied path
	if err != nil {
		exitWithMnemosError(f.Verbose, NewSystemError(err, "read %s", file))
		return
	}
	var exp workspaceExport
	if err := yaml.Unmarshal(data, &exp); err != nil {
		exitWithMnemosError(f.Verbose, NewUserError("parse %s: %v", file, err))
		return
	}
	name := strings.TrimSpace(exp.Name)
	if name == "" {
		exitWithMnemosError(f.Verbose, NewUserError("%s has no workspace name", file))
		return
	}
	src := exp.Folders
	if len(folders) > 0 {
		src = folders // --folder overrides the (machine-specific) recorded paths
	}
	abs := make([]string, 0, len(src))
	for _, d := range src {
		a, err := filepath.Abs(d)
		if err != nil {
			exitWithMnemosError(f.Verbose, NewUserError("bad folder %q: %v", d, err))
			return
		}
		abs = append(abs, filepath.Clean(a))
	}
	// The source's db path is machine-specific (and for a hosted brain the tenant,
	// not the db, is the shared identity), so it's informational only: default to a
	// machine-local brain, honoring the global --db override.
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
	if p, ok := sqliteFilePath(db); ok {
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
	}
	fmt.Printf("Imported workspace %q\n  folders: %s\n  brain:   %s\n  tenant:  %s  (same as source — shared)\n",
		name, strings.Join(abs, ", "), db, deriveHostedTenant(name))
	if len(abs) == 0 {
		fmt.Println("\nNo folders set — activate it explicitly with: mnemos workspace use " + name)
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
	unpinned := false
	if reg.Active == name {
		reg.Active = "" // don't leave a pin dangling at a removed workspace
		unpinned = true
	}
	if err := saveWorkspaceRegistry(reg); err != nil {
		exitWithMnemosError(f.Verbose, NewSystemError(err, "save workspace registry"))
		return
	}
	fmt.Printf("Removed workspace %q (its brain file is left in place).\n", name)
	if unpinned {
		fmt.Println("It was pinned; activation is back to folder-based.")
	}
}
