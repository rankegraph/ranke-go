// package: cmd/scenariodoc / cli
// type:    cmd
// job:     regenerate conformance/scenarios/*/scenario.md from each scenario's main.go comments
// limits:  doesn't run scenarios; only extracts their doc comments (-> conformance/scenarios)
//
// scenariodoc regenerates conformance/scenarios/*/scenario.md from
// comments in each scenario's main.go plus the template at
// conformance/helpers/scenario.md.tmpl. Run via `make scenarios-docs`.
package main

import (
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

var (
	errDirName = errors.New("scenariodoc: dir name mismatch")
	errParse   = errors.New("scenariodoc: parse")
)

func main() {
	root := flag.String("root", "conformance/scenarios", "scenarios root directory")
	tmplPath := flag.String("template", "conformance/helpers/scenario.md.tmpl", "scenario.md template")
	flag.Parse()

	tmpl, err := template.ParseFiles(*tmplPath)
	if err != nil {
		fail("parse template %s: %v", *tmplPath, err)
	}

	entries, err := os.ReadDir(*root)
	if err != nil {
		fail("read scenarios root: %v", err)
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, filepath.Join(*root, e.Name()))
		}
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		mainGo := filepath.Join(dir, "main.go")
		if _, err := os.Stat(mainGo); err != nil {
			fmt.Fprintf(os.Stderr, "scenariodoc: skipping %s (no main.go)\n", dir)
			continue
		}
		data, err := extractScenario(dir, mainGo)
		if err != nil {
			fail("%s: %v", dir, err)
		}
		out := filepath.Join(dir, "scenario.md")
		f, err := os.Create(out)
		if err != nil {
			fail("write %s: %v", out, err)
		}
		if err := tmpl.Execute(f, data); err != nil {
			f.Close()
			fail("render %s: %v", out, err)
		}
		f.Close()
		fmt.Printf("scenariodoc: %s\n", out)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "scenariodoc: "+format+"\n", args...)
	os.Exit(1)
}

// Scenario carries everything the template needs.
type Scenario struct {
	Number   string
	Title    string
	Intro    string
	Sections []Section
}

// Section is one numbered "--- N. Title ---" block of a Scenario.
type Section struct {
	Num   string
	Title string
	Body  string
}

var (
	dirRe     = regexp.MustCompile(`^(\d+)[_-](.+)$`)
	sectionRe = regexp.MustCompile(`^---\s*(\d+)\.\s*(.+?)\s*---$`)
)

func extractScenario(dir, mainGo string) (Scenario, error) {
	base := filepath.Base(dir)
	m := dirRe.FindStringSubmatch(base)
	if m == nil {
		return Scenario{}, fmt.Errorf("%w: %q doesn't match NN_name pattern", errDirName, base)
	}
	scn := Scenario{
		Number: m[1],
		Title:  strings.ReplaceAll(m[2], "_", " "),
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainGo, nil, parser.ParseComments)
	if err != nil {
		return Scenario{}, fmt.Errorf("%w: %s: %w", errParse, mainGo, err)
	}

	// Intro: comment groups whose End is before file.Package. Go
	// only attaches file.Doc when there's no blank line between
	// the comment and `package`; scenarios leave one for readability.
	// The canonical four-field file header (a "package:" block sitting
	// directly above package) is structural metadata, not prose, so it
	// is skipped here.
	var intro []string
	for _, cg := range file.Comments {
		if cg.End() < file.Package && !isFileHeader(cg.Text()) {
			intro = append(intro, commentText(cg.Text()))
		}
	}
	scn.Intro = strings.Join(intro, "\n\n")

	// Sections: comment groups opening "--- N. Title ---". A title long enough to
	// wrap closes on a later line, so the marker is matched over the joined group
	// rather than its first line — otherwise a wrapped step vanishes from the docs
	// with nothing to say it did.
	for _, cg := range file.Comments {
		if cg.End() < file.Package {
			continue
		}
		lines := splitLines(cg.Text())
		if len(lines) == 0 {
			continue
		}
		title, rest := splitSection(lines)
		if title == nil {
			continue
		}
		scn.Sections = append(scn.Sections, Section{
			Num:   title[1],
			Title: collapse(title[2]),
			Body:  commentText(strings.Join(rest, "\n")),
		})
	}
	return scn, nil
}

// splitSection matches the "--- N. Title ---" marker across as many leading lines as
// it spans, returning the submatches and the lines after it.
func splitSection(lines []string) ([]string, []string) {
	for n := 1; n <= len(lines); n++ {
		joined := strings.Join(lines[:n], " ")
		if sm := sectionRe.FindStringSubmatch(joined); sm != nil {
			return sm, lines[n:]
		}
	}
	return nil, nil
}

// collapse squeezes the runs of whitespace that joining wrapped lines leaves behind.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// commentText cleans up text extracted from a go/ast CommentGroup:
// trims trailing whitespace per line, collapses runs of blank lines,
// trims leading and trailing blanks.
func commentText(s string) string {
	lines := splitLines(s)
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// isFileHeader reports whether a comment group is the canonical
// four-field file header (its first non-blank line starts with the
// "package:" label), so it can be excluded from scenario prose.
func isFileHeader(s string) bool {
	for _, l := range splitLines(s) {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		return strings.HasPrefix(l, "package:")
	}
	return false
}

func splitLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}
