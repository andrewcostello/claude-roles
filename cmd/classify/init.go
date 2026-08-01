package main

// `classify init` scaffolds a project's rule table by looking at what is
// actually in the repository.
//
// It can detect *structure* — modules, client apps, generated output, tests,
// migrations, infrastructure. It cannot detect *meaning*: which of those paths
// move money, settle bets, or decide whether something is allowed. That is a
// human judgment about a specific business, and a tool that guesses it would be
// worse than no tool, because a wrong "financial: false" silently disables the
// human PR gate.
//
// So the scaffold is honest about what it does not know. It writes the
// structural rules it can justify, leaves clearly-marked TODO rules for the
// money and auth paths, and sets "scaffold": true — which makes classify force
// the human gate and the full panel on every change until someone completes the
// table. The generated config is immediately usable and maximally cautious; the
// only way to get proportionate classification is to do the thinking.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// detected is what a repository scan turned up.
type detected struct {
	GoModules  []string
	ClientApps []string
	Generated  []string
	Tests      []string
	Migrations []string
	Infra      []string
	Docs       bool
}

func cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	worktree := fs.String("worktree", ".", "Repository to scan")
	out := fs.String("out", "", "Where to write the config (default: <worktree>/.claude/risk-paths.json)")
	force := fs.Bool("force", false, "Overwrite an existing config")
	_ = fs.Parse(args)

	target := *out
	if target == "" {
		target = filepath.Join(*worktree, ".claude", "risk-paths.json")
	}
	if fileExists(target) && !*force {
		fmt.Printf("=== CLASSIFY INIT: REFUSING TO OVERWRITE ===\n")
		fmt.Printf("  %s already exists. Pass -force to replace it.\n", target)
		fmt.Println("  A hand-tuned rule table is the safety-critical part of this system;")
		fmt.Println("  overwriting one silently would discard reviewed judgments.")
		return exitInvalid
	}

	det := scanRepo(*worktree)
	cfg := scaffoldConfig(det)

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		return exitInvalid
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil { // #nosec G301 -- project config dir
		fmt.Fprintf(os.Stderr, "create %s: %v\n", filepath.Dir(target), err)
		return exitInvalid
	}
	// #nosec G306 -- a project config other tools and humans read.
	if err := os.WriteFile(target, append(data, '\n'), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", target, err)
		return exitInvalid
	}

	printInitReport(target, det)
	return 0
}

// scanRepo walks the top of the tree looking for structure worth a rule. It is
// deliberately shallow: deep scanning invites over-specific rules that rot.
func scanRepo(worktree string) detected {
	var d detected
	seen := map[string]bool{}

	add := func(list *[]string, p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			*list = append(*list, p)
		}
	}

	for _, dir := range []string{"apps", "libs", "services", "packages", "cmd", "."} {
		root := filepath.Join(worktree, dir)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "node_modules" {
				continue
			}
			rel := e.Name()
			if dir != "." {
				rel = dir + "/" + e.Name()
			}
			switch {
			case fileExists(filepath.Join(worktree, rel, "go.mod")):
				add(&d.GoModules, rel)
			case isClientApp(filepath.Join(worktree, rel)):
				add(&d.ClientApps, rel)
			}
		}
	}

	for _, probe := range []struct {
		path string
		list *[]string
	}{
		{"tests", &d.Tests}, {"test", &d.Tests}, {"e2e", &d.Tests},
		{"terraform", &d.Infra}, {"deploy", &d.Infra}, {"infra", &d.Infra}, {"k8s", &d.Infra},
		{"migrations", &d.Migrations}, {"db/migrations", &d.Migrations},
	} {
		if dirExists(filepath.Join(worktree, probe.path)) {
			add(probe.list, probe.path)
		}
	}

	for _, g := range []string{"generated", "gen", "pb"} {
		for _, base := range append([]string{"."}, d.GoModules...) {
			if dirExists(filepath.Join(worktree, base, g)) {
				add(&d.Generated, strings.TrimPrefix(base+"/"+g, "./"))
			}
		}
	}

	d.Docs = dirExists(filepath.Join(worktree, "docs"))

	sort.Strings(d.GoModules)
	sort.Strings(d.ClientApps)
	return d
}

func isClientApp(dir string) bool {
	pkg := filepath.Join(dir, "package.json")
	if !fileExists(pkg) {
		return false
	}
	data, err := os.ReadFile(pkg) // #nosec G304 -- discovered package.json under the scanned worktree
	if err != nil {
		return false
	}
	body := string(data)
	for _, marker := range []string{"react-native", "\"react\"", "next", "vite", "@angular", "vue"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// scaffoldConfig turns a scan into a config that is usable immediately and
// maximally cautious until reviewed.
func scaffoldConfig(d detected) Config {
	cfg := Config{
		SchemaVersion:           schemaVersion,
		Scaffold:                true,
		UnmatchedRisk:           "high",
		ServerSurfaceExtensions: []string{".go", ".java", ".py", ".rb", ".sql", ".proto", ".tf"},
	}

	// The rules a human MUST write. They are emitted with paths that match
	// nothing so the config stays valid, and named so they are impossible to
	// miss. Until they are filled in, scaffold:true keeps everything gated.
	cfg.Rules = append(cfg.Rules,
		Rule{
			ID:         "TODO-money-paths",
			Note:       "REPLACE THESE PATHS. Every path that moves, holds, settles or refunds money. No tool can infer this — it is the single most important rule in the table, and a wrong answer here silently disables the human PR gate. Add the component preset that matches (wallet, bet-settlement, bet-placement, jackpot).",
			Paths:      []string{"REPLACE-ME/never-matches/**"},
			Risk:       "critical",
			Components: []string{"wallet"},
			Financial:  true,
		},
		Rule{
			ID:    "TODO-auth-paths",
			Note:  "REPLACE THESE PATHS. Authentication, session, permission and capability code — anywhere a fail-open decides whether something is allowed.",
			Paths: []string{"REPLACE-ME/never-matches-auth/**"},
			Risk:  "high",
		},
	)

	for _, m := range d.GoModules {
		cfg.Rules = append(cfg.Rules, Rule{
			ID:    "module-" + slug(m),
			Note:  "Detected Go module. Raise to critical and add financial:true if it holds money logic.",
			Paths: []string{m + "/**"},
			Risk:  "high",
		})
	}
	if len(d.ClientApps) > 0 {
		var paths []string
		for _, a := range d.ClientApps {
			paths = append(paths, a+"/**")
		}
		cfg.Rules = append(cfg.Rules, Rule{
			ID:           "client-presentation",
			Note:         "Detected client apps. This is the ONLY reduced-panel carve-out. Before relying on it, add a companion rule marking wallet/withdraw/deposit/card screens NON-presentation — client money UI displays balances and submits movements, and a display bug is still a money bug.",
			Paths:        paths,
			Risk:         "low",
			Presentation: true,
		})
	}
	if len(d.Generated) > 0 {
		var paths []string
		for _, g := range d.Generated {
			paths = append(paths, g+"/**")
		}
		cfg.Rules = append(cfg.Rules, Rule{
			ID:    "generated-and-wire",
			Note:  "Generated output and wire contracts — never presentation.",
			Paths: append(paths, "**/*.proto", "**/*_pb.*", "**/pb/**"),
			Risk:  "high",
		})
	}
	if len(d.Migrations) > 0 || true {
		cfg.Rules = append(cfg.Rules, Rule{
			ID:        "schema-migrations",
			Note:      "Any migration or raw SQL. Routes the migration checklist deterministically.",
			Paths:     []string{"**/migrations/**", "**/*.sql"},
			Risk:      "high",
			Migration: true,
		})
	}
	if len(d.Infra) > 0 {
		var paths []string
		for _, i := range d.Infra {
			paths = append(paths, i+"/**")
		}
		cfg.Rules = append(cfg.Rules, Rule{
			ID:    "infrastructure",
			Note:  "Deployment and infrastructure config — a bad value here is a production incident.",
			Paths: append(paths, "**/*.tf", "**/*.tfvars", "**/Dockerfile*"),
			Risk:  "high",
		})
	}
	if len(d.Tests) > 0 {
		var paths []string
		for _, t := range d.Tests {
			paths = append(paths, t+"/**")
		}
		cfg.Rules = append(cfg.Rules, Rule{
			ID:           "test-harness",
			Paths:        paths,
			Risk:         "low",
			Presentation: true,
		})
	}
	cfg.Rules = append(cfg.Rules,
		Rule{
			ID:    "ci-and-scripts",
			Note:  "CI and repo scripts are gates themselves — a change here can DISABLE a check rather than fail one, so this is not presentation.",
			Paths: []string{".github/**", "scripts/**", "**/Makefile"},
			Risk:  "medium",
		},
		Rule{
			ID:           "docs-and-meta",
			Paths:        []string{"**/*.md", "**/README*", "docs/**", ".gitignore", "**/.gitignore"},
			Risk:         "low",
			Presentation: true,
		},
		Rule{
			ID:    "dependency-manifests",
			Note:  "Dependency changes are supply-chain surface — never low.",
			Paths: []string{"**/go.mod", "**/go.sum", "**/package-lock.json", "**/pnpm-lock.yaml", "**/requirements*.txt", "**/pom.xml"},
			Risk:  "medium",
		})

	cfg.GateSignals = defaultGateSignals()
	return cfg
}

// defaultGateSignals are content patterns that revoke the reduced-panel
// carve-out. They are business-agnostic: every codebase has flags and guards.
func defaultGateSignals() []GateSignal {
	return []GateSignal{
		{ID: "env-gate", Note: "Environment-driven behaviour switch.", Pattern: `os\.Getenv\(|process\.env\.|System\.getenv\(|os\.environ`},
		{ID: "dev-build-gate", Note: "Build-mode gate — ships enabled if the flag leaks to a deployed env.", Pattern: `__DEV__|NODE_ENV|IS_DEBUG|isDebug|DEBUG_|devMode|debugMode`},
		{ID: "feature-flag", Note: "Feature flag or kill switch.", Pattern: `[Ff]eature[Ff]lag|featureEnabled|isEnabled\(|flagEnabled|killSwitch`},
		{ID: "override-bypass", Note: "Explicit bypass or override path.", Pattern: `[Bb]ypass|skipAuth|skipCheck|allowAll`},
		{ID: "permission-decision", Note: "Authorization decision points.", Pattern: `[Cc]anAccess|[Ii]sAllowed|hasPermission|[Aa]uthorize\(|[Ii]sAdmin|requireRole`},
	}
}

func slug(s string) string {
	r := strings.NewReplacer("/", "-", "_", "-", ".", "-", " ", "-")
	return strings.Trim(r.Replace(s), "-")
}

func printInitReport(target string, d detected) {
	fmt.Println("=== CLASSIFY INIT ===")
	fmt.Printf("Wrote %s\n\n", target)

	fmt.Println("Detected:")
	report := []struct {
		label string
		items []string
	}{
		{"Go modules", d.GoModules},
		{"Client apps", d.ClientApps},
		{"Generated output", d.Generated},
		{"Test harnesses", d.Tests},
		{"Infrastructure", d.Infra},
		{"Migrations", d.Migrations},
	}
	for _, r := range report {
		if len(r.items) > 0 {
			fmt.Printf("  %-18s %s\n", r.label+":", strings.Join(r.items, " "))
		}
	}

	fmt.Println("\n⚠ THIS CONFIG IS A SCAFFOLD AND IS NOT FINISHED.")
	fmt.Println()
	fmt.Println("  Structure can be detected. MEANING cannot: no tool can know which of")
	fmt.Println("  your paths move money or decide what is allowed. Two rules are stubs:")
	fmt.Println()
	fmt.Println("    TODO-money-paths   every path that moves, holds, settles or refunds money")
	fmt.Println("    TODO-auth-paths    auth, session, permission and capability code")
	fmt.Println()
	fmt.Println("  Until you replace their paths and set \"scaffold\": false, every change")
	fmt.Println("  gets the human PR gate and the full panel. That is deliberate — a table")
	fmt.Println("  nobody has reviewed cannot certify anything as safe, and the failure")
	fmt.Println("  mode of guessing would be a silently-skipped gate on a money path.")
	fmt.Println()
	fmt.Println("  Then verify against your own history:")
	fmt.Println("    for sha in $(git log --merges --format=%H -50); do")
	fmt.Println("      git diff $sha^1 $sha | classify -json -no-git | jq -r .unmatched_files[]?")
	fmt.Println("    done | sort | uniq -c | sort -rn")
	fmt.Println()
	fmt.Println("  Every path that prints is a rule you are missing.")
}
