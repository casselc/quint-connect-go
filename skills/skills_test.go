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
	if len(list) < 4 {
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
}
