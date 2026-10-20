// Package skills embeds quint-connect-go's agent skills (Agent Skills
// format: one directory per skill with a SKILL.md), so they can be
// installed into any project: `quintgo skills install` writes them to
// .claude/skills (or -dir), where Claude Code and other agents that read
// Agent Skills pick them up.
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

//go:embed */SKILL.md
var files embed.FS

// FS holds the skills: <name>/SKILL.md.
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

// Install writes every skill to dir/<name>/SKILL.md. Existing files are
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
