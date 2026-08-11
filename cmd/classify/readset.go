// Consumer read-set generation and the v1 differential (unit B1).
//
// SCAFFOLD — CONTRACTS ONLY. Bodies panic. The doc comments are the spec.
//
// Why this file is a generator and not a list:
//
// An earlier informal enumeration of what the frozen consumers read named
// skills, config_path and panel.reasons — none of which any consumer declares —
// and OMITTED recheck_min_severity, which cmd/iterate genuinely reads as its
// severity floor (iterate/main.go:271-272). A v1 emitter built from that list
// would have dropped the floor, iterate's floorFor would have returned its
// "high" fallback (iterate/main.go:274), and every MEDIUM finding would have
// been skipped silently on exactly the critical-system runs the floor exists
// for. The list was wrong in both directions at once, and nothing detected it.
//
// So the read-set is DERIVED FROM THE CONSUMERS' SOURCE, every time, and the
// derivation carries file:line for every field so no human ever hand-cites it
// again. Citations in prose go stale; this effort has already caught several.
//
// The citations in these comments were re-verified against the worktree at
// scaffold time, not copied from the design document.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// ─── what "read" means ───────────────────────────────────────────────────────

// ReadKind distinguishes the two defensible definitions of a consumer's read
// set. The design says "generated from the consumers' ASTs" and does not say
// which. CHOICE: both are computed; DECLARED is normative for emitter totality
// and REFERENCED is advisory.
type ReadKind int

const (
	// ReadKindUnset is the zero value. Illegal. Raise.
	ReadKindUnset ReadKind = iota

	// ReadKindDeclared: the field appears as a JSON tag on the consumer's own
	// closed struct declaration for the classification payload.
	//
	// This is NORMATIVE for emitter totality, for two reasons. First, a
	// declared field is what survives the consumer's unmarshal-and-marshal-back
	// cycle, so it is what the consumer PRESERVES for whoever reads the file
	// next. Second, a declared-but-currently-unreferenced field is one
	// consumer edit away from being referenced, with no producer change to
	// notice it — whereas adding a field to the struct is a visible edit that
	// this generator will pick up on its next run.
	ReadKindDeclared

	// ReadKindReferenced: the field is additionally reached by a selector
	// expression in the consumer's code (state.Classification.X, cls.X).
	//
	// ADVISORY. It answers "which fields change behaviour today", which is
	// useful for triaging a differential diff, and it is NOT the emitter's
	// obligation. An emitter built to the referenced set only would be exactly
	// the hand-list failure with an AST in front of it.
	ReadKindReferenced
)

// ─── the generated set ───────────────────────────────────────────────────────

// SourceSite is a citation the generator produced, not one a human typed.
type SourceSite struct {
	// File is repo-relative, e.g. "cmd/iterate/main.go".
	File string
	Line int
	// What names the site: "declared" for the struct field declaration,
	// "referenced" for a selector expression.
	What string
}

// ReadField is one field of the classification payload that at least one frozen
// consumer declares.
type ReadField struct {
	// JSONPath is the dotted path within the `classification` object, using
	// the JSON tag names, with [] for a slice element:
	// "risk", "components", "changed_files[].path".
	//
	// Nested structs ARE descended: cmd/gates declares FileClass with only
	// `path` (gates/main.go:128-130), while the producer's FileClass carries
	// path, risk and rules (main.go:159-163). The generator must report
	// changed_files[].path and must NOT report changed_files[].risk, or the
	// emitter's obligation would be overstated.
	JSONPath string

	// Consumers names every consumer declaring this field, sorted, e.g.
	// ["gates", "iterate"]. The union across consumers is the emitter's
	// obligation; the per-consumer attribution is what makes a regression
	// report actionable.
	Consumers []string

	// Kind is ReadKindDeclared for every entry. Referenced is recorded in
	// Sites rather than as a separate entry, so a field appears exactly once.
	Kind ReadKind

	// Referenced is true iff at least one consumer reaches this field with a
	// selector expression. Advisory. False here is NOT permission to omit the
	// field from the v1 emission.
	Referenced bool

	// Sites carries every declaration and every reference, generated.
	Sites []SourceSite
}

// ReadSet is the generator's whole output.
type ReadSet struct {
	// Fields is sorted by JSONPath so the output is deterministic and can be
	// checked in as a golden that a consumer edit visibly breaks.
	Fields []ReadField
	// Consumers is the set that was actually parsed, sorted. A ReadSet whose
	// Consumers list is short is a ReadSet that was generated against an
	// incomplete world; see the hard-failure rule on GenerateReadSet.
	Consumers []string
}

// ConsumerSource names one frozen consumer to parse.
type ConsumerSource struct {
	// Name is the short name used in ReadField.Consumers, e.g. "gates".
	Name string
	// Dir is the directory holding the consumer's package, relative to the
	// classify package directory. classify is its own Go module and does not
	// and must not import these packages; the generator reads their SOURCE.
	Dir string
	// TypeName is the consumer's closed struct for the classification payload.
	// Both consumers call it "Classification" today, but the generator takes
	// it as data rather than assuming the name, because a rename in a frozen
	// consumer must produce a hard failure and not an empty read set.
	TypeName string
}

// frozenConsumers is the world the generator parses. It is the ONLY hand-
// written part of this mechanism, and it is deliberately hand-written: which
// programs are frozen consumers of the classification is a design fact, not a
// derivable one. What must never be hand-written is which FIELDS they read.
//
// Verified against the worktree at scaffold time:
//   - cmd/gates    declares Classification{risk, components, changed_files}
//     at gates/main.go:121-126, with FileClass{path} at :128-130.
//   - cmd/iterate  declares Classification{risk, components,
//     recheck_min_severity, reviewer_args} at iterate/main.go:86-91.
//
// Adding a consumer here is a design decision. Removing one is the removal of a
// constraint on the emitter and must be justified in the commit that does it.
var frozenConsumers = []ConsumerSource{
	{Name: "gates", Dir: "../gates", TypeName: "Classification"},
	{Name: "iterate", Dir: "../iterate", TypeName: "Classification"},
}

// GenerateReadSet parses each consumer's source and returns the union read set.
//
// HARD-FAILURE RULE, which is the whole safety property: if any consumer's
// directory is missing, fails to parse, or does not declare TypeName, this
// returns an error. It must NEVER return a partial ReadSet. A generator that
// degrades to a smaller set when it cannot see a consumer reproduces the
// hand-list bug with more machinery — the omission would be invisible and the
// emitter would drop a field that a consumer still reads.
//
// Likewise it must error on a declared field whose JSON tag is absent,
// malformed, or "-": an untagged Go field marshals under its Go name, which is
// a different wire key, and guessing between the two is exactly the class of
// silent mismatch this generator exists to prevent.
//
// It parses with go/parser at the file level. It does NOT type-check and does
// not need to: it never imports the consumer modules, which is what keeps
// cmd/classify independent of them.
//
// Determinism is contractual: two runs over identical sources produce identical
// ReadSets, including Sites order. The output is meant to be committed and
// diffed.
func GenerateReadSet(consumers []ConsumerSource) (ReadSet, error) {
	if len(consumers) == 0 {
		return ReadSet{}, fmt.Errorf("read set: no consumers named — an empty world yields an empty read set, and an empty read set makes the emitter's totality obligation and the differential's domain both vacuous")
	}

	merged := map[string]*ReadField{}
	names := make([]string, 0, len(consumers))
	for _, c := range consumers {
		fields, err := consumerReadFields(c)
		if err != nil {
			// Every return on this path is the ZERO ReadSet. A partial set is
			// worse than no set: a caller that only checks the error would be
			// fine, and a caller that used the value would silently emit less
			// than a consumer reads.
			return ReadSet{}, err
		}
		names = append(names, c.Name)
		for _, f := range fields {
			existing, ok := merged[f.JSONPath]
			if !ok {
				field := f
				merged[f.JSONPath] = &field
				continue
			}
			existing.Consumers = append(existing.Consumers, f.Consumers...)
			existing.Sites = append(existing.Sites, f.Sites...)
			existing.Referenced = existing.Referenced || f.Referenced
		}
	}

	rs := ReadSet{
		Fields:    make([]ReadField, 0, len(merged)),
		Consumers: dedupeSorted(names),
	}
	for _, f := range merged {
		f.Consumers = dedupeSorted(f.Consumers)
		f.Sites = dedupeSortedSites(f.Sites)
		rs.Fields = append(rs.Fields, *f)
	}
	// Sorted by JSONPath: the output is meant to be committed and diffed, and a
	// generator whose order wobbled would produce a diff every run and train
	// everyone to ignore it.
	sort.Slice(rs.Fields, func(i, j int) bool { return rs.Fields[i].JSONPath < rs.Fields[j].JSONPath })
	return rs, nil
}

// consumerReadFields is the whole read set of ONE consumer, or an error.
func consumerReadFields(c ConsumerSource) ([]ReadField, error) {
	fset, files, err := parseConsumerPackage(c)
	if err != nil {
		return nil, err
	}
	structs := packageStructs(files)
	decl, ok := structs[c.TypeName]
	if !ok {
		return nil, fmt.Errorf("consumer %q: %s declares no struct type %q. TypeName is taken as data precisely so a RENAME in a frozen consumer is a hard failure and not an empty read set", c.Name, c.Dir, c.TypeName)
	}

	refs := selectorSites(fset, files)
	var out []ReadField
	if err := collectDeclaredFields(c, fset, structs, decl, "", map[string]bool{c.TypeName: true}, refs, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("consumer %q: struct %q declares no wire fields — a consumer that reads nothing is not a consumer, and treating it as one would silently shrink the emitter's obligation", c.Name, c.TypeName)
	}
	return out, nil
}

// parseConsumerPackage reads and parses one consumer's non-test Go source.
//
// It parses at the FILE level with go/parser and never type-checks, which is
// what keeps cmd/classify from importing the consumer modules — classify is its
// own Go module and must stay independent of the programs it is measured
// against.
//
// _test.go files are excluded: the classification is read by the consumer's
// production code, and a test fixture's struct is not an obligation on the
// producer's wire.
func parseConsumerPackage(c ConsumerSource) (*token.FileSet, []*ast.File, error) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("consumer %q: cannot read %s: %w — a generator that degrades when it cannot see a consumer is the hand-list bug with an AST in front of it", c.Name, c.Dir, err)
	}
	var goFiles []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		goFiles = append(goFiles, name)
	}
	// Sorted, because Sites order is contractual and it is derived from the
	// order files are walked in.
	sort.Strings(goFiles)
	if len(goFiles) == 0 {
		return nil, nil, fmt.Errorf("consumer %q: no non-test Go source in %s", c.Name, c.Dir)
	}

	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(goFiles))
	for _, name := range goFiles {
		p := filepath.Join(c.Dir, name)
		f, parseErr := parser.ParseFile(fset, p, nil, 0)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("consumer %q: parse %s: %w — half a file is not half a read set", c.Name, p, parseErr)
		}
		files = append(files, f)
	}
	return fset, files, nil
}

// packageStructs indexes every struct type the consumer declares, so a nested
// field's type can be resolved without type-checking.
func packageStructs(files []*ast.File) map[string]*ast.StructType {
	out := map[string]*ast.StructType{}
	for _, f := range files {
		for _, d := range f.Decls {
			gen, ok := d.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if st, ok := ts.Type.(*ast.StructType); ok {
					out[ts.Name.Name] = st
				}
			}
		}
	}
	return out
}

// collectDeclaredFields walks one struct, descending into nested struct types
// the same package declares.
//
// prefix is either empty or already ends in ".". A nested struct contributes
// only its LEAVES: cmd/gates declares FileClass with `path` only, so the set
// carries changed_files[].path and never changed_files, because reporting the
// container would overstate the emitter's obligation.
func collectDeclaredFields(
	c ConsumerSource,
	fset *token.FileSet,
	structs map[string]*ast.StructType,
	decl *ast.StructType,
	prefix string,
	descending map[string]bool,
	refs map[string][]SourceSite,
	out *[]ReadField,
) error {
	for _, field := range decl.Fields.List {
		if len(field.Names) != 1 {
			// Embedded fields and grouped declarations both land here. Neither
			// has one unambiguous wire key, and guessing one is the silent
			// mismatch this generator exists to prevent.
			return fmt.Errorf("consumer %q: %s has a field that is embedded or shares its tag with another name; the generator does not guess at its wire key", c.Name, c.TypeName)
		}
		goName := field.Names[0].Name

		key, err := jsonWireKey(c, goName, field)
		if err != nil {
			return err
		}
		path := prefix + key

		if nested, name, isSlice, ok := nestedStruct(structs, field.Type); ok {
			if descending[name] {
				return fmt.Errorf("consumer %q: struct %q is recursive through %s; the wire has no such shape", c.Name, name, path)
			}
			childPrefix := path
			if isSlice {
				childPrefix += "[]"
			}
			descending[name] = true
			err := collectDeclaredFields(c, fset, structs, nested, childPrefix+".", descending, refs, out)
			delete(descending, name)
			if err != nil {
				return err
			}
			continue
		}

		// The declared citation points at the TAG's line, which is the line a
		// reader has to look at to check the wire key.
		sites := []SourceSite{siteAt(fset, field.Tag.Pos(), "declared")}
		sites = append(sites, refs[goName]...)

		*out = append(*out, ReadField{
			JSONPath:   path,
			Consumers:  []string{c.Name},
			Kind:       ReadKindDeclared,
			Referenced: len(refs[goName]) > 0,
			Sites:      sites,
		})
	}
	return nil
}

// jsonWireKey extracts the field's wire key, or fails.
//
// Absent, malformed, empty and "-" tags are all errors and not defaults: an
// untagged Go field marshals under its Go NAME, which is a different wire key,
// and a "-" tag has no wire key at all. Guessing between them is exactly the
// class of silent mismatch this generator exists to prevent.
func jsonWireKey(c ConsumerSource, goName string, field *ast.Field) (string, error) {
	if field.Tag == nil {
		return "", fmt.Errorf("consumer %q: %s.%s has no struct tag — an untagged field marshals under its Go name, which is a different wire key", c.Name, c.TypeName, goName)
	}
	raw, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return "", fmt.Errorf("consumer %q: %s.%s has an unreadable struct tag %s: %w", c.Name, c.TypeName, goName, field.Tag.Value, err)
	}
	tag, ok := reflect.StructTag(raw).Lookup("json")
	if !ok {
		return "", fmt.Errorf("consumer %q: %s.%s has a struct tag with no readable json key (%q) — a malformed tag is not an empty one", c.Name, c.TypeName, goName, raw)
	}
	key := tag
	if i := strings.Index(key, ","); i >= 0 {
		key = key[:i]
	}
	switch key {
	case "":
		return "", fmt.Errorf("consumer %q: %s.%s has a json tag with no name (%q) — it would marshal under its Go name", c.Name, c.TypeName, goName, tag)
	case "-":
		return "", fmt.Errorf("consumer %q: %s.%s is tagged json:\"-\" and has no wire key at all", c.Name, c.TypeName, goName)
	}
	return key, nil
}

// nestedStruct resolves a field's type to a struct the same package declares,
// looking through pointers and one level of slice or array.
//
// The bool it returns for slices is what puts the "[]" in changed_files[].path:
// the wire path has to say that the leaf lives inside every element, or the
// differential could not compare the elements in order.
func nestedStruct(structs map[string]*ast.StructType, expr ast.Expr) (decl *ast.StructType, name string, isSlice bool, ok bool) {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.ArrayType:
			isSlice = true
			expr = t.Elt
		case *ast.Ident:
			st, found := structs[t.Name]
			if !found {
				return nil, "", false, false
			}
			return st, t.Name, isSlice, true
		default:
			return nil, "", false, false
		}
	}
}

// selectorSites indexes every selector expression in the consumer's source by
// the name it selects.
//
// This is the ADVISORY half. It answers "which fields change behaviour today",
// which is useful for triaging a differential diff and is explicitly NOT the
// emitter's obligation — matching a selector by name alone can over-report, and
// over-reporting an advisory flag is harmless in a way that under-reporting the
// declared set is not.
func selectorSites(fset *token.FileSet, files []*ast.File) map[string][]SourceSite {
	out := map[string][]SourceSite{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			out[sel.Sel.Name] = append(out[sel.Sel.Name], siteAt(fset, sel.Sel.Pos(), "referenced"))
			return true
		})
	}
	for name := range out {
		out[name] = dedupeSortedSites(out[name])
	}
	return out
}

func siteAt(fset *token.FileSet, pos token.Pos, what string) SourceSite {
	p := fset.Position(pos)
	return SourceSite{File: repoRelative(p.Filename), Line: p.Line, What: what}
}

// repoRelative renders a citation the way a human cites one: "cmd/gates/main.go".
//
// The root is found by walking up for a .git entry — a FILE in a worktree, a
// directory in a normal clone — so a citation means the same thing from any
// working directory and in any checkout of this repository.
func repoRelative(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	for dir := filepath.Dir(abs); ; {
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil {
			if rel, relErr := filepath.Rel(dir, abs); relErr == nil {
				return filepath.ToSlash(rel)
			}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.ToSlash(p)
}

func dedupeSorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return slicesCompactStrings(out)
}

func slicesCompactStrings(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// dedupeSortedSites gives Sites a total, stable order. Determinism is
// contractual here: two runs over identical sources must produce byte-identical
// ReadSets, Sites order included.
func dedupeSortedSites(in []SourceSite) []SourceSite {
	out := append([]SourceSite(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].What < out[j].What
	})
	compact := out[:0]
	for i, s := range out {
		if i == 0 || s != out[i-1] {
			compact = append(compact, s)
		}
	}
	return compact
}

// EmitterCovers checks that a v1 emission covers the whole generated read set.
//
// It reports every JSONPath in rs that does not appear as a key in the emitted
// v1 payload, and it must treat an omitempty-elided key as MISSING when the
// value is the type's zero — because for the consumer, an absent key and a
// zero value are the same bytes, and the emitter's totality claim is about
// bytes.
//
// EXPECTED RESULT AT BASELINE, stated so a seal can pin it: the read set is
// exactly {risk, components, changed_files[].path, recheck_min_severity,
// reviewer_args}, and the v1 emission covers all five. If a future run produces
// a sixth, someone edited a frozen consumer and the emitter must be re-checked
// before that edit ships.
func EmitterCovers(rs ReadSet, emittedV1 []byte) (missing []string, err error) {
	if len(rs.Fields) == 0 {
		return nil, fmt.Errorf("emitter coverage: the read set is empty, so \"covers everything\" would be a claim about nothing")
	}
	var doc any
	if err := json.Unmarshal(emittedV1, &doc); err != nil {
		return nil, fmt.Errorf("emitter coverage: the v1 emission does not parse: %w", err)
	}

	for _, f := range rs.Fields {
		value, present := resolveJSONPath(doc, parseJSONPath(f.JSONPath))
		if !present || isVacantValue(value) {
			// An omitempty-elided key counts as MISSING. For the consumer an
			// absent key and a zero value are the same bytes, and the emitter's
			// totality claim is about bytes.
			missing = append(missing, f.JSONPath)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// isVacantValue reports a resolved value that demonstrates nothing.
//
// The only case is an element path over an empty or gap-ridden array:
// changed_files[].path is not covered by `"changed_files": []`, because no
// element carries the key the consumer reads, and it is not covered when some
// element lacks it either.
func isVacantValue(v any) bool {
	elems, ok := v.([]any)
	if !ok {
		return false
	}
	if len(elems) == 0 {
		return true
	}
	for _, e := range elems {
		if _, absent := e.(absentValue); absent {
			return true
		}
	}
	return false
}

// ─── JSON path resolution ────────────────────────────────────────────────────

// pathSegment is one step of a ReadField.JSONPath. `slice` marks a "[]" step,
// where the remaining segments are resolved inside EVERY element.
type pathSegment struct {
	key   string
	slice bool
}

func parseJSONPath(p string) []pathSegment {
	parts := strings.Split(p, ".")
	segs := make([]pathSegment, 0, len(parts))
	for _, part := range parts {
		seg := pathSegment{key: part}
		if strings.HasSuffix(part, "[]") {
			seg.key = strings.TrimSuffix(part, "[]")
			seg.slice = true
		}
		segs = append(segs, seg)
	}
	return segs
}

// absentValue marks a path that was not present.
//
// It is a distinct type rather than nil because JSON `null` is a real value a
// document can carry, and collapsing "the key is not there" into "the key is
// null" would reintroduce, inside the differential itself, the absent-means-zero
// state this whole unit exists to close.
type absentValue struct{}

// resolveJSONPath walks segs through doc.
//
// For a "[]" segment it returns one resolved value PER ELEMENT, in document
// order, so that a slice of objects compares element-wise the way the contract
// requires. present is about the named key, not about the elements: a present
// but empty array is present.
func resolveJSONPath(doc any, segs []pathSegment) (any, bool) {
	cur := doc
	for i, seg := range segs {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := obj[seg.key]
		if !ok {
			return nil, false
		}
		if !seg.slice {
			cur = next
			continue
		}
		elems, ok := next.([]any)
		if !ok {
			return nil, false
		}
		rest := segs[i+1:]
		out := make([]any, 0, len(elems))
		for _, e := range elems {
			v, elemPresent := resolveJSONPath(e, rest)
			if !elemPresent {
				out = append(out, absentValue{})
				continue
			}
			out = append(out, v)
		}
		return out, true
	}
	return cur, true
}

// ─── the v1 differential ─────────────────────────────────────────────────────

// volatileV1Fields is the named exclusion set for the differential against the
// pinned old binary. Byte identity is impossible and was never the goal.
//
// classified_at is the only member: the old binary stamps the wall clock at
// main.go:274 into the field declared at main.go:141-143. It is a volatile
// derived field, it is the reason the v2 envelope drops it, and excluding it is
// what makes the comparison a test that can pass.
//
// This list is a wire-contract change if it grows. Adding a field here means
// declaring that the two binaries may legitimately disagree about it, which is
// a much stronger claim than "this test is flaky". Any addition needs the same
// review as a schema change.
var volatileV1Fields = []string{"classified_at"}

// DifferentialResult is one comparison of two v1 emissions.
type DifferentialResult struct {
	// Equivalent is true iff every compared path agrees.
	Equivalent bool
	// Divergences names each disagreeing JSON path with both values, in
	// sorted path order.
	Divergences []Divergence
	// Compared names every path that was compared, so a vacuous pass — a
	// comparison that happened to look at nothing — is visible in the result
	// rather than hidden behind Equivalent: true. A seal MUST assert that
	// Compared is non-empty and contains the read set; this field exists
	// because a seal that only asserts Equivalent is a vacuous seal.
	Compared []string
	// Excluded names every path skipped as volatile, so the exclusion is
	// visible in the output rather than implied by its absence.
	Excluded []string
}

// Divergence is one disagreement.
type Divergence struct {
	JSONPath string
	Old      string
	New      string
}

// SemanticEquivalentV1 compares the pinned old binary's v1 output against the
// new binary's v1 output over the GENERATED read set, excluding volatile.
//
// Contract:
//   - The comparison domain is exactly rs, minus volatile. It is not "all keys
//     present in both", which would let a field the new binary stopped emitting
//     pass unnoticed, and it is not "all keys in old", which would fail on
//     harmless additions.
//   - A path present in rs but absent from exactly ONE document is a divergence,
//     not a skip: absence is a value, and a value only one side carries is a
//     disagreement. A path absent from BOTH documents is COMPARED and AGREES —
//     two absences are the same bytes, and nothing else would let the
//     differential pass on a real fixture, because `components,omitempty`
//     elides that key on both sides of the docs-only classification.
//     Symmetric absence agreeing here is not a hole: totality is EmitterCovers'
//     job, where an elided key IS missing. That is why docs-only passes this
//     comparison while EmitterCovers reports components missing for the same
//     bytes. The two ask different questions — this one asks whether two
//     producers agree, that one asks whether one emission is total — and
//     collapsing them would cost one of the answers.
//   - Comparison is semantic, not textual: JSON numbers compare numerically,
//     slices compare element-wise in order (the producer's ordering is already
//     deterministic — components via sortedKeys at main.go:737, risk_reasons in
//     fixed tier order at main.go:741), objects compare key-wise.
//   - It errors if rs is empty, if either document fails to parse, or if
//     volatile names a path not in rs. A differential over an empty domain
//     would report Equivalent: true having compared nothing.
//
// The two documents must be produced from IDENTICAL inputs: same config bytes,
// same diff bytes, same flags. Producing them from a live worktree makes
// resolveRepo's git state an uncontrolled input; the fixture must use -no-git.
func SemanticEquivalentV1(oldOut, newOut []byte, rs ReadSet, volatile []string) (DifferentialResult, error) {
	if len(rs.Fields) == 0 {
		return DifferentialResult{}, fmt.Errorf("differential: the read set is empty, so the comparison would report Equivalent: true having compared nothing")
	}

	inReadSet := map[string]bool{}
	for _, f := range rs.Fields {
		inReadSet[f.JSONPath] = true
	}

	// The membership check is unconditional and has real work to do: it catches
	// a typo'd or stale exclusion, and an exclusion list is a list of
	// PERMISSIONS TO DISAGREE. A permission to disagree about a path nobody
	// compares is not a permission — and tolerating it would make
	// Excluded: ["classified_at"] read identically whether the path was in the
	// domain and silenced or never in the domain at all, which is the
	// implicit-state collapse this unit exists to close.
	excluded := map[string]bool{}
	for _, v := range volatile {
		if !inReadSet[v] {
			return DifferentialResult{}, fmt.Errorf("differential: volatile names %q, which is not in the read set. An exclusion that silences nothing must not be indistinguishable from one that silences a field; if no consumer declares it, pass no exclusion at all", v)
		}
		excluded[v] = true
	}

	var oldDoc, newDoc any
	if err := json.Unmarshal(oldOut, &oldDoc); err != nil {
		return DifferentialResult{}, fmt.Errorf("differential: the OLD document does not parse: %w", err)
	}
	if err := json.Unmarshal(newOut, &newDoc); err != nil {
		return DifferentialResult{}, fmt.Errorf("differential: the NEW document does not parse: %w", err)
	}

	res := DifferentialResult{
		Compared: make([]string, 0, len(rs.Fields)),
		Excluded: make([]string, 0, len(volatile)),
	}
	for path := range excluded {
		res.Excluded = append(res.Excluded, path)
	}
	sort.Strings(res.Excluded)

	// The domain is exactly rs minus volatile. Not "all keys present in both",
	// which would let a field the new binary stopped emitting pass unnoticed,
	// and not "all keys in old", which would fail on harmless additions.
	domain := make([]string, 0, len(rs.Fields))
	for _, f := range rs.Fields {
		if !excluded[f.JSONPath] {
			domain = append(domain, f.JSONPath)
		}
	}
	sort.Strings(domain)
	if len(domain) == 0 {
		return DifferentialResult{}, fmt.Errorf("differential: every path in the read set is excluded as volatile, leaving nothing to compare")
	}

	for _, path := range domain {
		segs := parseJSONPath(path)
		oldVal, oldPresent := resolveJSONPath(oldDoc, segs)
		newVal, newPresent := resolveJSONPath(newDoc, segs)
		res.Compared = append(res.Compared, path)

		switch {
		case !oldPresent && !newPresent:
			// Two absences are the same bytes. Compared, and agreeing.
		case oldPresent != newPresent:
			res.Divergences = append(res.Divergences, Divergence{
				JSONPath: path,
				Old:      renderJSONValue(oldVal, oldPresent),
				New:      renderJSONValue(newVal, newPresent),
			})
		case !reflect.DeepEqual(oldVal, newVal):
			// Semantic, not textual: both documents were decoded to any, so
			// every JSON number is a float64 and 5 equals 5.0; slices compare
			// element-wise in order, and objects key-wise.
			res.Divergences = append(res.Divergences, Divergence{
				JSONPath: path,
				Old:      renderJSONValue(oldVal, true),
				New:      renderJSONValue(newVal, true),
			})
		}
	}

	res.Equivalent = len(res.Divergences) == 0
	return res, nil
}

// renderJSONValue is a divergence's human-readable side.
//
// Absence renders as a word rather than as "" or "null", because a differential
// report whose two sides read "" and "" would be describing the one difference
// that matters here as no difference at all.
func renderJSONValue(v any, present bool) string {
	if !present {
		return "<absent>"
	}
	b, err := json.Marshal(replaceAbsentMarkers(v))
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// replaceAbsentMarkers makes an element-wise resolution printable: the marker
// type does not marshal, and inside a slice it means one element lacked the key.
func replaceAbsentMarkers(v any) any {
	elems, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(elems))
	for _, e := range elems {
		if _, absent := e.(absentValue); absent {
			out = append(out, "<absent>")
			continue
		}
		out = append(out, e)
	}
	return out
}

// ─── sidecar survival ────────────────────────────────────────────────────────

// SidecarSurvives asserts the property the separate-file decision rests on:
// after the frozen pipeline rewrites the shared run-state, the v2 sidecar is
// untouched and still parses.
//
// It runs classify → gates → iterate over one run-state and reports whether the
// sidecar's bytes are identical before and after. Identical, not merely
// parseable: the sidecar has exactly one writer and any change to it means
// something else opened it.
//
// BASELINE FINDING THE SEAL AUTHOR MUST KNOW — measured in this worktree, not
// reasoned from line numbers:
//
// The v1 projection does NOT survive the frozen pipeline today. cmd/gates
// unmarshals the run-state into a Classification declaring only three fields
// (gates/main.go:121-126) and marshals the whole state back
// (gates/main.go:1329). Running classify then gates over one run-state reduces
// the classification object from 15 keys to 2. recheck_min_severity,
// reviewer_args, human_pr_gate, panel, financial_paths_touched, config_path and
// skills are all destroyed by the first gate round.
//
// Two consequences that are NOT hypothetical:
//   - iterate's floorFor (iterate/main.go:270-275) then finds no
//     recheck_min_severity and returns "high", skipping MEDIUM findings — the
//     exact failure the read-set generator was built to prevent, arriving by a
//     different route.
//   - README's step 3 reads .classification.reviewer_args with jq AFTER gates
//     has run, and gets nothing.
//
// So the design's claim that a fixture "asserts both projections survive every
// rewrite" is false for the v1 projection at baseline. This function must
// therefore assert the SIDECAR's survival — which is real, because nothing else
// writes that file — and must report the v1 projection's loss as a measurement
// rather than assert it away. Do not "fix" this by widening the frozen
// consumers' structs: cmd/gates and cmd/iterate are frozen, and the differential
// is only meaningful while they stay untouched.
// HOW THIS ONE EXECUTES, and why that is the whole implementation:
//
// A body that stats the run-state, runs NOTHING, and returns a loss list read
// off cmd/gates' struct declaration would report survived=true — precisely
// BECAUSE it ran nothing, so the sidecar's bytes are trivially unchanged. That
// stub was built and it passed the seal as originally written. So the survival
// reported here is only ever a survival of a rewrite that REALLY HAPPENED: the
// frozen consumers are executed as child processes over the caller's run-state,
// and the loss is measured by reading that file before and after. Nothing here
// may be derived from a struct declaration, and nothing may be hardcoded.
//
// WHAT v1KeysLost CONTAINS: top-level keys of the `classification` object that
// were present before the pipeline and are absent after. changed_files[].risk
// and changed_files[].rules are destroyed too, inside a key that survives — a
// real loss this list cannot name, recorded as a finding rather than smuggled
// into a list whose every member must be checkable against the file.
func SidecarSurvives(runState string) (survived bool, v1KeysLost []string, err error) {
	before, err := runStateClassificationKeys(runState)
	if err != nil {
		return false, nil, err
	}
	if len(before) == 0 {
		return false, nil, fmt.Errorf("sidecar survival: run-state %s carries no classification keys — there is no loss to measure and no rewrite to survive, so any verdict would be a claim about nothing", runState)
	}

	sidecarPath := V2SidecarPath(runState)
	// #nosec G304 -- derived from the run-state path the caller named.
	sidecarBefore, err := os.ReadFile(sidecarPath)
	if err != nil {
		return false, nil, fmt.Errorf("sidecar survival: cannot read the sidecar at %s: %w — survival is a comparison of bytes before and after, and there is nothing to compare", sidecarPath, err)
	}

	pipeline, err := frozenConsumerPipeline(frozenConsumers, runState)
	if err != nil {
		return false, nil, err
	}
	for _, inv := range pipeline {
		if err := inv.run(); err != nil {
			return false, nil, err
		}
	}

	after, err := runStateClassificationKeys(runState)
	if err != nil {
		return false, nil, err
	}
	lost := keysMissingFrom(after, before)

	// #nosec G304 -- the same derived path read above.
	sidecarAfter, err := os.ReadFile(sidecarPath)
	if err != nil {
		return false, lost, fmt.Errorf("sidecar survival: the sidecar at %s is gone after the pipeline: %w", sidecarPath, err)
	}
	// Identical, not merely parseable: this file has exactly one writer, so any
	// change to it means something else opened it.
	return bytes.Equal(sidecarBefore, sidecarAfter), lost, nil
}

// consumerInvocation is one frozen consumer, ready to run over a run-state.
type consumerInvocation struct {
	name string
	bin  string
	args []string
	// verdictExits marks a consumer whose non-zero exit is a VERDICT and not a
	// failure. cmd/iterate exits 1 for ITERATE and 2 for ESCALATE, and treating
	// either as a crash would abort the measurement on a healthy run.
	verdictExits bool
}

// frozenConsumerPipeline derives how to execute each frozen consumer.
//
// The binary is <Dir>/<Name>, derived from the same hand-written world list
// GenerateReadSet parses, so adding a consumer moves both together. It is not a
// pinned path: pinning "../gates/gates" here would still not prove anything ran,
// while a consumer that has quietly vanished would be skipped.
//
// Every way of not being able to run a consumer is a HARD FAILURE. An unknown
// consumer name raises rather than being skipped — the exhaustive-switch rule
// again: adding a third frozen consumer must break this loudly, because a
// pipeline that silently omits one measures a loss that is not the real one.
//
// Consumers run in frozenConsumers order, which is the pipeline order
// classify → gates → iterate. The classify leg is the CALLER's: the run-state
// it hands in is already classify's output, and re-running classify would need
// a diff and a config this function is not given.
func frozenConsumerPipeline(consumers []ConsumerSource, runState string) ([]consumerInvocation, error) {
	if len(consumers) == 0 {
		return nil, fmt.Errorf("sidecar survival: no frozen consumers to run — a pipeline of nothing rewrites nothing, and its survival would mean nothing")
	}
	out := make([]consumerInvocation, 0, len(consumers))
	for _, c := range consumers {
		bin := filepath.Join(c.Dir, c.Name)
		if _, err := os.Stat(bin); err != nil {
			return nil, fmt.Errorf("sidecar survival: frozen consumer %q has no binary at %s: %w — it is a tracked fixture, and a measurement that skipped it would certify a survival it never observed", c.Name, bin, err)
		}
		switch c.Name {
		case "gates":
			cfg, err := soleConsumerTestdataConfig(c)
			if err != nil {
				return nil, err
			}
			// -only nosuchgate: what is being measured is the run-state
			// REWRITE, not any gate's verdict. gates still performs the
			// readRunState → MarshalIndent cycle that destroys the keys, which
			// is the whole mechanism, while running no project command in a
			// temp directory that has none.
			out = append(out, consumerInvocation{
				name: c.Name,
				bin:  bin,
				args: []string{"-run-state", runState, "-config", cfg, "-only", "nosuchgate"},
			})
		case "iterate":
			out = append(out, consumerInvocation{
				name:         c.Name,
				bin:          bin,
				args:         []string{"next", "-run-state", runState},
				verdictExits: true,
			})
		default:
			return nil, fmt.Errorf("sidecar survival: no invocation is known for frozen consumer %q. It was added to frozenConsumers without saying how it reads a run-state, and guessing would produce a measurement of a pipeline that never ran", c.Name)
		}
	}
	return out, nil
}

// soleConsumerTestdataConfig discovers the consumer's config by finding the one
// JSON file under its testdata directory.
//
// Discovery with a hard failure, rather than a pinned filename: zero files or
// two files both raise, so a renamed or duplicated fixture is reported instead
// of silently changing which table the measurement ran against.
func soleConsumerTestdataConfig(c ConsumerSource) (string, error) {
	dir := filepath.Join(c.Dir, "testdata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("sidecar survival: consumer %q needs a config and %s cannot be read: %w", c.Name, dir, err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		found = append(found, filepath.Join(dir, e.Name()))
	}
	sort.Strings(found)
	if len(found) != 1 {
		return "", fmt.Errorf("sidecar survival: consumer %q needs exactly one config under %s, found %d %v — discovery raises rather than choosing, because choosing would make the measurement depend on a filename nobody declared", c.Name, dir, len(found), found)
	}
	return found[0], nil
}

func (inv consumerInvocation) run() error {
	// #nosec G204 -- bin is derived from frozenConsumers, this package's own
	// hand-written world list, and the arguments are literals plus the
	// run-state path the caller named. No shell is involved.
	cmd := exec.Command(inv.bin, inv.args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if inv.verdictExits && errors.As(err, &exitErr) {
		// The process ran to completion and returned a verdict. That is the
		// rewrite happening, which is what is being measured.
		return nil
	}
	return fmt.Errorf("sidecar survival: %s did not run: %w\n%s", inv.name, err, out.String())
}

// runStateClassificationKeys reads the top-level key set of the run-state's
// classification object, sorted.
//
// Deliberately map[string]json.RawMessage and not the Classification struct:
// unmarshalling into a closed struct is the very mechanism that destroys keys
// in the frozen consumers, and a measurement that used it could not see the
// keys it exists to count.
func runStateClassificationKeys(runState string) ([]string, error) {
	// #nosec G304 -- the run-state path the caller named.
	data, err := os.ReadFile(runState)
	if err != nil {
		return nil, fmt.Errorf("sidecar survival: cannot read run-state %s: %w — an unobserved survival is not a survival", runState, err)
	}
	var state struct {
		Classification map[string]json.RawMessage `json:"classification"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("sidecar survival: run-state %s is not valid JSON: %w", runState, err)
	}
	keys := make([]string, 0, len(state.Classification))
	for k := range state.Classification {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// keysMissingFrom returns the members of want that got does not carry, sorted.
func keysMissingFrom(got, want []string) []string {
	have := make(map[string]bool, len(got))
	for _, g := range got {
		have[g] = true
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	sort.Strings(missing)
	return missing
}
