package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing/fstest"

	"github.com/vmaltarello/whyd/internal/rules"
	builtin "github.com/vmaltarello/whyd/rules"
)

// runRules handles "whyd rules" (list) and "whyd rules test [path...]".
func runRules(args []string) error {
	if len(args) > 0 && args[0] == "test" {
		return testRules(args[1:])
	}
	if len(args) > 0 {
		return fmt.Errorf("unknown rules command %q (try: whyd rules, whyd rules test)", args[0])
	}
	set, err := rules.LoadDefault(userRuleDirs()...)
	if err != nil {
		return err
	}
	fmt.Printf("%-24s %-9s %-6s %s\n", "ID", "LEVEL", "KIND", "FROM")
	for _, r := range set.Rules {
		from := r.Origin
		if r.Overrides != "" {
			from += " (replaces " + r.Overrides + ")"
		}
		fmt.Printf("%-24s %-9s %-6s %s\n", r.ID, r.Severity, r.Kind, from)
	}
	where := "~/.config/whyd/rules"
	if dirs := userRuleDirs(); len(dirs) > 0 {
		where = dirs[0]
	}
	fmt.Printf("\n%d rules. Your own rules go in %s (*.toml); the format is in\n%s\n",
		len(set.Rules), where, "https://github.com/vmaltarello/whyd/blob/main/docs/RULES.md")
	for _, p := range set.Problems {
		fmt.Println("skipped:", p)
	}
	return nil
}

// testRules loads the built-in rules plus the given files or directories (default: the user's
// rule directory) and runs every example. It fails when an example fails or a rule is invalid.
func testRules(paths []string) error {
	sources := []rules.Source{{Name: "built-in", FS: builtin.Builtin, Strict: true}}
	explicit := len(paths) > 0
	if !explicit {
		paths = userRuleDirs()
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		switch {
		case err != nil && explicit:
			return err
		case err != nil:
			continue // no user directory yet: only the built-in rules
		case fi.IsDir():
			sources = append(sources, rules.Source{Name: p, FS: os.DirFS(p)})
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sources = append(sources, rules.Source{
				Name: filepath.Dir(p),
				FS:   fstest.MapFS{filepath.Base(p): {Data: data}},
			})
		}
	}
	set, err := rules.Load(sources...)
	if err != nil {
		return err
	}
	failures, examples := 0, 0
	for _, f := range set.Test() {
		fmt.Println("FAIL", f)
		failures++
	}
	for _, r := range set.Rules {
		examples += len(r.Examples)
		if len(r.Examples) == 0 && r.Origin != "built-in" {
			fmt.Printf("note %s (%s): no [[rule.example]], add one real line it must match\n", r.ID, r.Origin)
		}
	}
	for _, p := range set.Problems {
		fmt.Println("SKIP", p)
		failures++
	}
	if failures > 0 {
		if failures == 1 {
			return fmt.Errorf("1 problem in %d rules", len(set.Rules))
		}
		return fmt.Errorf("%d problems in %d rules", failures, len(set.Rules))
	}
	fmt.Printf("ok: %d rules, %d examples\n", len(set.Rules), examples)
	return nil
}
