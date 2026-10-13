package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing/fstest"

	"github.com/vmaltarello/whyd/internal/rules"
	builtin "github.com/vmaltarello/whyd/rules"
)

const rulesGuide = "https://github.com/vmaltarello/whyd/blob/main/docs/RULES.md"

// runRules lists the rules in use, built in and the user's, and checks them: the mistakes that
// made whyd skip a rule, examples that don't do what they say, rules that can never fire. With
// files or directories, those are checked instead of the user's rule directory, before they are
// put in place. It fails when there is a problem.
func runRules(paths []string) error {
	sources := []rules.Source{{Name: "built-in", FS: builtin.Builtin, Strict: true}}
	explicit := len(paths) > 0
	if !explicit {
		paths = userRuleDirs()
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		switch {
		case err != nil && explicit:
			return fmt.Errorf("no rule file or directory %q", p)
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

	fmt.Printf("%-24s %-8s %s\n", "ID", "LEVEL", "FROM")
	row := func(r *rules.Rule, level string) {
		from := r.Origin
		if r.Changes != "" {
			from += " (changes " + r.Changes + ")"
		}
		fmt.Printf("%-24s %-8s %s\n", r.ID, level, from)
	}
	for _, r := range set.Rules {
		row(r, r.Level.String())
	}
	for _, r := range set.Off {
		row(r, "off")
	}
	fmt.Println()

	problems, examples := 0, 0
	for _, p := range set.Problems {
		fmt.Println("SKIP", p)
		problems++
	}
	for _, f := range set.Test() {
		fmt.Println("FAIL", f)
		problems++
	}
	for _, r := range set.Rules {
		examples += len(r.Match) + len(r.NoMatch)
		if len(r.Match) == 0 && r.Origin != "built-in" {
			fmt.Printf("note %s (%s): no example, add a real line it must match: match = [\"...\"]\n", r.ID, r.Origin)
		}
	}
	if !explicit {
		where := "~/.config/whyd/rules"
		if dirs := userRuleDirs(); len(dirs) > 0 {
			where = dirs[0]
		}
		fmt.Printf("Your own rules go in %s (*.toml); the format is in\n%s\n", where, rulesGuide)
	}
	switch problems {
	case 0:
		fmt.Printf("ok: %d rules, %d examples\n", len(set.Rules), examples)
		return nil
	case 1:
		return fmt.Errorf("1 problem in %d rules", len(set.Rules))
	}
	return fmt.Errorf("%d problems in %d rules", problems, len(set.Rules))
}
