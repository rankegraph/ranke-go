// package: cmd/ranke / cli
// type:    cmd
// job:     read-only CLI to inspect filesystem-backed Ranke-Graph archives
// limits:  no mutation commands; building claims lives in tests + downstream apps (-> tests)
//
// ranke opens a filesystem-backed archive at <dir> and runs read-only queries
// against it. Run it without arguments for the subcommands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage/fs"
	"github.com/rankegraph/ranke-go/internal/vectors"
	"github.com/spf13/cobra"
)

var (
	errOpen     = errors.New("open archive")
	errShow     = errors.New("show")
	errValidate = errors.New("validate")
	// errNoBookmark: a bookmark id is given, never discovered (foundation paper
	// §Backup). Without one this archive cannot be opened at all.
	errNoBookmark = errors.New("no bookmark id at dir/branches/B_h — an archive cannot be opened without one")
)

// dirHelp describes the one argument every command shares.
const dirHelp = "dir is a data bundle: dir/universe/ (claims, content and bookmarks) plus dir/branches/B_h (one bookmark id)."

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ranke:", err)
		os.Exit(1)
	}
}

// newRootCmd builds the command tree. Errors print once, prefixed, without usage —
// a failed read is not a misuse.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "ranke",
		Short:         "Ranke-Graph archive inspector",
		Long:          "ranke reads a filesystem-backed Ranke-Graph archive.\n\n" + dirHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(infoCmd(), branchesCmd(), showCmd(), validateCmd())
	return root
}

// openArchive opens the bundle at dir — fs Universe and 𝒰_hist over one directory,
// plus the bookmark id at dir/branches/B_h — at its latest recorded head. The
// Universe comes back too, for closure walks over it directly.
func openArchive(ctx context.Context, dir string) (ranke.Universe, ranke.Archive, error) {
	universe := filepath.Join(dir, "universe")
	u, err := fs.New(universe)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: universe in %s: %w", errOpen, dir, err)
	}
	id, err := readBookmarkId(filepath.Join(dir, "branches", "B_h"))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errOpen, err)
	}
	marks, err := ranke.OpenBookmarks(ctx, u, id)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open bookmark list in %s: %w", errOpen, dir, err)
	}
	head, err := marks.Latest(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: latest head in %s: %w", errOpen, dir, err)
	}
	arc, err := ranke.NewArchive(ctx, u, head.Head())
	if err != nil {
		return nil, nil, err
	}
	return u, arc, nil
}

// readBookmarkId reads the bookmark id a bundle carries at path. It is given rather
// than discovered (foundation paper §Backup), so a missing, empty, or unparsable file
// is refused outright rather than treated as an empty archive.
func readBookmarkId(path string) (ranke.Id, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errNoBookmark, path, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil, fmt.Errorf("%w: %s is empty", errNoBookmark, path)
	}
	id, err := ranke.ParseId(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errNoBookmark, path, err)
	}
	return id, nil
}

// infoCmd summarises an archive: its branch count and each head.
func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <dir>",
		Short: "Summarise an archive: branch count and heads",
		Long:  "Summarise an archive.\n\n" + dirHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdInfo(cmd.Context(), args)
		},
	}
}

func cmdInfo(ctx context.Context, args []string) error {
	_, a, err := openArchive(ctx, args[0])
	if err != nil {
		return err
	}
	branches, err := a.GetBranches(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("archive %s\n", args[0])
	fmt.Printf("  branches: %d\n", len(branches))
	for _, b := range branches {
		fmt.Printf("    %s → %s\n", b.Name(), shortId(b.Head()))
	}
	return nil
}

// branchesCmd lists every branch with its full head id.
func branchesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "branches <dir>",
		Short: "List every branch and its head id",
		Long:  "List every branch and its head id.\n\n" + dirHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdBranches(cmd.Context(), args)
		},
	}
}

func cmdBranches(ctx context.Context, args []string) error {
	_, a, err := openArchive(ctx, args[0])
	if err != nil {
		return err
	}
	bs, err := a.GetBranches(ctx)
	if err != nil {
		return err
	}
	if len(bs) == 0 {
		fmt.Println("(no branches)")
		return nil
	}
	for _, b := range bs {
		fmt.Printf("%-20s %s\n", b.Name(), b.Head().String())
	}
	return nil
}

// showCmd prints one claim, reached either way: a file on disk, or an id resolved
// through an archive.
func showCmd() *cobra.Command {
	var idFlag string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <file> | <dir> <id>",
		Short: "Print one claim, from a file or by id",
		Long: "Print one claim, reached either way:\n\n" +
			"  show <file>       read the file directly\n" +
			"  show <dir> <id>   resolve the id through the archive at dir\n\n" +
			"A claim record holds no id of its own, so a file is read as a claim only\n" +
			"when its name is an id or --id supplies one. Otherwise it prints as content.\n\n" +
			dirHelp,
		Example: "  ranke show data/universe/b5uaxbgs...\n" +
			"  ranke show data b5uaxbgs...\n" +
			"  ranke show testdata/cbor/claims/derived-note.cbor --id b5uazx2e...\n" +
			"  ranke show data b5uaxbgs... --json",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 {
				if idFlag != "" {
					return fmt.Errorf("%w: --id is for a single file; <dir> <id> already names one", errShow)
				}
				return showInArchive(cmd.Context(), args[0], args[1], asJSON)
			}
			return showFile(args[0], idFlag, asJSON)
		},
	}
	cmd.Flags().StringVar(&idFlag, "id", "", "the claim's id, when the file is not named by one")
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the claim as JSON instead of a field listing")
	return cmd
}

// idUnknown is what a record read without its id prints in the id's place. A claim
// record carries no id, so this is the honest answer, not a failure.
const idUnknown = "unknown (not carried in the record)"

// showFile reads path as a claim, falling back to a content dump only when the bytes
// are not a claim record at all.
func showFile(path, idOverride string, asJSON bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: %w", errShow, err)
	}

	// The id comes from --id, else the filename in an archive's flat layout, else a
	// manifest naming this file. Absent all three, a placeholder lets the record
	// decode and the id prints as unknown.
	idText := idUnknown
	id, idErr := ranke.ParseId(idOverride)
	switch {
	case idOverride != "" && idErr != nil:
		return fmt.Errorf("%w: parse --id %q: %w", errShow, idOverride, idErr)
	case idOverride != "":
		idText = id.String()
	default:
		if fromName, err := ranke.ParseId(filepath.Base(path)); err == nil {
			id, idText = fromName, fromName.String()
		} else if fromManifest, ok := idFromManifest(path); ok {
			id, idText = fromManifest, fromManifest.String()
		} else if id, err = ranke.HashContent(nil); err != nil {
			return fmt.Errorf("%w: %w", errShow, err)
		}
	}

	c, err := ranke.DecodeClaim(id, b)
	if err != nil {
		fmt.Printf("file:  %s\nkind:  content (%d bytes)\n\n", path, len(b))
		fmt.Println(previewBytes(b))
		return nil
	}
	fmt.Printf("file:  %s\nkind:  claim (%d bytes)\n\n", path, len(b))
	return render(c, idText, asJSON)
}

// idFromManifest looks for a reference-artifact manifest at or above path's directory
// and reports the id it names for this file, saying what it found and why.
func idFromManifest(path string) (ranke.Id, bool) {
	m, root, ok := vectors.Find(path)
	if !ok {
		return nil, false
	}
	c := m.ClaimFor(root, path)
	if c == nil {
		return nil, false
	}
	id, err := ranke.ParseId(c.Id)
	if err != nil {
		return nil, false
	}

	fmt.Printf("manifest: %s\n", filepath.Join(root, vectors.Name))
	if v := m.Provenance.Version; v != "" {
		fmt.Printf("          generated by %s %s\n", m.Provenance.Generator, v)
	}
	fmt.Printf("          %s has id %s\n", c.File, c.Id)
	outcome := "must be rejected"
	if c.Verify {
		outcome = "must verify"
	}
	fmt.Printf("          %s (%s) — %s\n\n", outcome, c.Reason, c.Why)
	return id, true
}

// render prints a claim as a field listing, or as the claim's own JSON.
func render(c ranke.Claim, idText string, asJSON bool) error {
	if !asJSON {
		printClaim(c, idText)
		return nil
	}
	raw, err := c.EncodeJSON(ranke.FormOriginal)
	if err != nil {
		return fmt.Errorf("%w: %w", errShow, err)
	}
	// The claim renders its own id, which a placeholder would misreport.
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("%w: %w", errShow, err)
	}
	fields["id"] = idText
	pretty, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %w", errShow, err)
	}
	fmt.Println(string(pretty))
	return nil
}

func showInArchive(ctx context.Context, dir, idStr string, asJSON bool) error {
	_, a, err := openArchive(ctx, dir)
	if err != nil {
		return err
	}
	id, err := ranke.ParseId(idStr)
	if err != nil {
		return fmt.Errorf("%w: parse id %q: %w", errShow, idStr, err)
	}
	c, err := a.GetClaim(ctx, id)
	if err != nil {
		return fmt.Errorf("%w: fetch claim: %w", errShow, err)
	}
	return render(c, id.String(), asJSON)
}

// validateCmd verifies every branch closure end-to-end (§5.10).
func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <dir>",
		Short: "Verify every branch closure end-to-end (§5.10)",
		Long: "Walk each branch's closure, verifying every claim's integrity and\n" +
			"authenticity, and print the tree with a mark per claim.\n\n" + dirHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdValidate(cmd.Context(), args)
		},
	}
}

func cmdValidate(ctx context.Context, args []string) error {
	u, a, err := openArchive(ctx, args[0])
	if err != nil {
		return err
	}
	branches, err := a.GetBranches(ctx)
	if err != nil {
		return err
	}
	if len(branches) == 0 {
		return fmt.Errorf("%w: archive has no branches", errValidate)
	}
	totalFailed := 0
	for _, b := range branches {
		fmt.Printf("branch %s → %s\n", b.Name(), b.Head().String())
		g, err := ranke.NewGraphFromClosure(ctx, b.Head(), u)
		if err != nil {
			fmt.Printf("  ✗ open closure: %v\n", err)
			totalFailed++
			continue
		}
		run := g.Verify()
		run.Wait()
		failByID := map[string]error{}
		for _, f := range run.Failures() {
			failByID[f.ID.String()] = f.Err
		}
		count := printClosure(ctx, g, failByID)
		fmt.Printf("  %d/%d claims valid\n", count-len(failByID), count)
		if run.Err() != nil {
			fmt.Printf("  ✗ walk: %v\n", run.Err())
			totalFailed++
		} else if len(failByID) > 0 {
			totalFailed++
		}
	}
	if totalFailed > 0 {
		return fmt.Errorf("%w: %d branch(es) failed", errValidate, totalFailed)
	}
	return nil
}

// printClosure walks from the open heads breadth-first, printing each claim once
// (✓, or ✗ with its error from failByID) indented by depth, and counts them.
func printClosure(ctx context.Context, g ranke.Graph, failByID map[string]error) int {
	type node struct {
		id    ranke.Id
		depth int
	}
	seen := map[string]bool{}
	queue := make([]node, 0)
	for _, h := range g.Heads() {
		queue = append(queue, node{h, 0})
	}
	count := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		key := n.id.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		c, err := g.GetClaim(ctx, n.id)
		if err != nil {
			continue
		}
		count++
		indent := strings.Repeat("  ", n.depth+1)
		if e, bad := failByID[key]; bad {
			fmt.Printf("%s✗ %s  %s\n%s     %v\n", indent, c.Node().Type(), shortId(n.id), indent, e)
		} else {
			fmt.Printf("%s✓ %s  %s\n", indent, c.Node().Type(), shortId(n.id))
		}
		for _, e := range c.Edges() {
			queue = append(queue, node{e.Reference(), n.depth + 1})
		}
	}
	return count
}

// --- helpers ---

func shortId(id ranke.Id) string {
	if id == nil {
		return "<nil>"
	}
	s := id.String()
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}

// printClaim lists a claim's ADT fields. idText overrides the printed id, so a record
// read without one says so.
func printClaim(c ranke.Claim, idText string) {
	n := c.Node()
	fmt.Printf("id:           %s\n", idText)
	fmt.Printf("type:         %s\n", n.Type())
	fmt.Printf("created_at:   %s\n", n.CreatedAt().Format(stampFormat))
	printContent(n, "")
	if enc := n.Encoding(); enc != "" {
		fmt.Printf("encoding:     %s\n", enc)
	}
	if c.Contributor() != nil {
		fmt.Printf("contributor:  %s\n", c.Contributor().ID().String())
	}

	edges := c.Edges()
	if len(edges) == 0 {
		return
	}
	// Edges carry no created_at of their own; the arrow is the claim they reference.
	fmt.Printf("edges (%d):\n", len(edges))
	for i, e := range edges {
		fmt.Printf("  [%d] id:         %s\n", i, e.ID().String())
		fmt.Printf("      type:       %s\n", e.Type())
		fmt.Printf("      → %s\n", e.Reference().String())
		printContent(e, "      ")
		if enc := e.Encoding(); enc != "" {
			fmt.Printf("      encoding:   %s\n", enc)
		}
		if e.RelationDirection() != 0 {
			dir := "from"
			if e.RelationDirection() == ranke.RelationTo {
				dir = "to"
			}
			fmt.Printf("      direction:  %s\n", dir)
		}
	}
}

// stampFormat renders created_at at the nanosecond precision the record holds.
const stampFormat = "2006-01-02T15:04:05.000000000Z"

// contentBearer is what nodes and edges share: content either inline or addressed.
type contentBearer interface {
	ContentKind() ranke.ContentKind
	GetContentHash() ranke.Id
	GetContentSize() uint64
	GetInlineContent() ([]byte, error)
}

// printContent lists the content fields of a node or an edge under one indent, so
// both read the same.
func printContent(b contentBearer, indent string) {
	switch b.ContentKind() {
	case ranke.ContentExternal:
		fmt.Printf("%scontent_hash: %s\n", indent, b.GetContentHash().String())
		fmt.Printf("%scontent_size: %d\n", indent, b.GetContentSize())
		fmt.Printf("%scontent:      (external, fetch by hash)\n", indent)
	case ranke.ContentInline:
		fmt.Printf("%scontent_size: %d\n", indent, b.GetContentSize())
		if raw, err := b.GetInlineContent(); err == nil && raw != nil {
			fmt.Printf("%scontent:      %s\n", indent, previewBytes(raw))
		}
	}
}

func previewBytes(b []byte) string {
	const max = 80
	s := string(b)
	if len(s) > max {
		s = s[:max] + "…"
	}
	// crude printability check
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Sprintf("(binary, %d bytes)", len(b))
		}
	}
	// strip trailing newlines for compactness
	return strings.TrimRight(s, "\n\r")
}
