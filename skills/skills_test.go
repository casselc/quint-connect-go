package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillsHaveFrontMatter(t *testing.T) {
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 6 {
		t.Fatalf("%d skills", len(list))
	}
	for _, s := range list {
		if len(s.Description) < 40 {
			t.Errorf("%s: description too short: %q", s.Name, s.Description)
		}
		b, _ := os.ReadFile(filepath.Join(s.Name, "SKILL.md"))
		if frontMatter(string(b), "name") != s.Name {
			t.Errorf("%s: front matter name must equal the directory name", s.Name)
		}
	}
	dir := t.TempDir()
	paths, err := Install(dir, false)
	if err != nil || len(paths) != len(list) {
		t.Fatalf("%v %v", paths, err)
	}
	if _, err := Install(dir, false); err == nil {
		t.Fatal("overwrote without force")
	}
	dirs, err := TargetDirs("p", nil)
	if err != nil || len(dirs) != 2 || dirs[0] != filepath.Join("p", ".agents", "skills") {
		t.Fatalf("default targets: %v %v", dirs, err)
	}
	if _, err := TargetDirs("p", []string{"nope"}); err == nil {
		t.Fatal("unknown target accepted")
	}
}

// The repository exposes the skills where agents look for them.
func TestRepoSkillDirs(t *testing.T) {
	list, _ := List()
	for _, d := range []string{"../.agents/skills", "../.claude/skills"} {
		for _, s := range list {
			if _, err := os.Stat(filepath.Join(d, s.Name, "SKILL.md")); err != nil {
				t.Errorf("%s/%s: %v (symlink it to ../../skills/%s)", d, s.Name, err, s.Name)
			}
		}
	}
}
