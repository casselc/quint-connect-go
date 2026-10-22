// Package skills embeds quint-connect-go's agent skills (Agent Skills
// format: one directory per skill with a SKILL.md), so they can be
// installed into any project with `quintgo skills install`. By default
// they go to the vendor-neutral .agents/skills, which Cursor, VS Code /
// Copilot, Codex and others read, and to .claude/skills for Claude Code.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed */SKILL.md */references/*.md
var files embed.FS

// FS holds the skills: <name>/SKILL.md, and the references a skill loads
// when a task needs them, <name>/references/*.md.
func FS() fs.FS { return files }

// Skill is one embedded skill.
type Skill struct {
	Name        string
	Description string
}

// List returns the embedded skills, sorted by name.
func List() ([]Skill, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(files, e.Name()+"/SKILL.md")
		if err != nil {
			return nil, err
		}
		out = append(out, Skill{Name: e.Name(), Description: frontMatter(string(b), "description")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func frontMatter(doc, key string) string {
	if !strings.HasPrefix(doc, "---\n") {
		return ""
	}
	end := strings.Index(doc[4:], "\n---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(doc[4:4+end], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Targets are the skills directories agents read, relative to a project
// (or, for a user-wide install, to the home directory).
var Targets = map[string]string{
	"agents": filepath.Join(".agents", "skills"), // vendor-neutral: Cursor, VS Code/Copilot, Codex, ...
	"claude": filepath.Join(".claude", "skills"), // Claude Code
	"github": filepath.Join(".github", "skills"), // GitHub Copilot
	"cursor": filepath.Join(".cursor", "skills"), // Cursor
}

// DefaultTargets are installed when none are named.
var DefaultTargets = []string{"agents", "claude"}

// TargetDirs resolves target names ("all" for every one) under root.
func TargetDirs(root string, names []string) ([]string, error) {
	if len(names) == 0 {
		names = DefaultTargets
	}
	if len(names) == 1 && names[0] == "all" {
		names = nil
		for n := range Targets {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	var out []string
	for _, n := range names {
		d, ok := Targets[n]
		if !ok {
			var known []string
			for k := range Targets {
				known = append(known, k)
			}
			sort.Strings(known)
			return nil, fmt.Errorf("unknown target %q (have %s, all)", n, strings.Join(known, ", "))
		}
		out = append(out, filepath.Join(root, d))
	}
	return out, nil
}

// Install writes every skill to dir/<name>/SKILL.md, with its references. Existing files are
// left alone unless force is set. It returns the paths written.
func Install(dir string, force bool) ([]string, error) {
	var written []string
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if _, err := os.Stat(dst); err == nil && !force {
			return fmt.Errorf("%s exists (use -force to overwrite)", dst)
		}
		b, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
		written = append(written, dst)
		return nil
	})
	return written, err
}
