package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSkillsHaveFrontMatter(t *testing.T) {
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 7 {
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
	refs, _ := fs.Glob(FS(), "*/references/*.md")
	if err != nil || len(paths) != len(list)+len(refs) {
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

// Every relative link in a skill or reference resolves among the
// installed files: a skill loads its references (and another skill's) by
// path.
func TestSkillLinksResolve(t *testing.T) {
	link := regexp.MustCompile(`\]\(([^)#:]+\.md)\)`)
	_ = fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(FS(), p)
		for _, m := range link.FindAllStringSubmatch(string(b), -1) {
			target := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(p), m[1])))
			if strings.HasPrefix(target, "..") {
				continue // outside the skills (the repository's docs)
			}
			if _, err := fs.Stat(FS(), target); err != nil {
				t.Errorf("%s links to %s, which is not installed with the skills", p, m[1])
			}
		}
		return nil
	})
}

// Skills teach the current API: an identifier removed from the code (or
// a report format it no longer prints) would send agents to it.
func TestSkillsAvoidDeprecated(t *testing.T) {
	deprecated := []struct{ text, instead string }{
		{"qtest.Check(", "qtest.Validate(...).Expect"},
		{"qtest.RecordAndCheck", "qtest.Record(...).Expect"},
		{"qtest.Explore(", "qtest.System{...}.Explore"},
		{"qtest.SweepExplore", "System.Sweep"},
		{"Explored", "qtest.Run"},
		{"qtest.Expect(", "Run.Expect (qtest.Validate(...).Expect)"},
		{"DurableFileSink", "qobs.FileSink (durable)"},
		{"qenv.Recorder", "qenv.ChoiceLog"},
		{"quinthegel.Recorder", "quinthegel.Case"},
		{"quinthegel.Chooser", "quinthegel.Choices"},
		{"quinthegel.Replay", "quinthegel.VaryTraces"},
		{"qenv.Faults(", "qenv.DeclareFaults("},
		{"qload.System", "qload.Target"},
		{"Chain.Expect", "Chain.Mean"},
		{".Expect(t, true)", "a named claim (qload.SomeTrigger, qload.Recovered)"},
		{".Expect(t, false)", "a named claim (qload.NoTrigger, qload.StaysDegraded)"},
		{"x.Coverage()", "x.Evidence().Faults"},
		{"x.CodeCoverage()", "x.Evidence().Code"},
		{"step [", `"step N" (reports number steps from 1)`},
	}
	_ = fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		b, _ := fs.ReadFile(FS(), p)
		for _, dep := range deprecated {
			if strings.Contains(string(b), dep.text) {
				t.Errorf("%s mentions %s, which is gone: use %s", p, dep.text, dep.instead)
			}
		}
		return nil
	})
}
