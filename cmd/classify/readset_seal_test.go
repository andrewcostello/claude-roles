package main

// Seals for readset.go — the generated consumer read-set, the v1 differential
// against the pinned binary, and the sidecar-survival measurement.
//
// The load-bearing seal in this file is the HARD-FAILURE lattice on
// GenerateReadSet. Everything else here trusts the read set: the differential's
// comparison domain is it, and the emitter's totality obligation is it. A
// generator that returned a smaller set when it could not see a consumer would
// make every other seal in this file quietly weaker, and nothing would say so.
// That is the hand-list bug with an AST in front of it, and it is the exact
// shape of the failure that already happened once: an informal list omitted
// recheck_min_severity, cmd/iterate's floorFor would have fallen back to
// "high", and every MEDIUM finding would have been skipped on precisely the
// critical-system runs the floor exists for.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// ─── synthetic consumers ─────────────────────────────────────────────────────

// synthConsumers writes a throwaway consumer package per entry and returns the
// ConsumerSource list pointing at them.
//
// They live under testdata/ so the Go tool never compiles them (the "broken"
// ones do not parse, by design) and are removed on cleanup. Dirs are relative
// to the classify package directory, which is what ConsumerSource.Dir means and
// what `go test`'s working directory is.
func synthConsumers(t *testing.T, files map[string]string) []ConsumerSource {
	t.Helper()
	base := filepath.Join("testdata", "readset-gen", sanitize(t.Name()))
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(base)
		// And the shared parent, once the last test has emptied it. Remove
		// fails harmlessly while another case still holds a directory there.
		_ = os.Remove(filepath.Dir(base))
	})

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	names = sortedCopy(names)

	out := make([]ConsumerSource, 0, len(files))
	for _, name := range names {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(files[name]), 0o600); err != nil {
			t.Fatal(err)
		}
		out = append(out, ConsumerSource{Name: name, Dir: dir, TypeName: "Classification"})
	}
	return out
}

var nonWord = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func sanitize(s string) string { return nonWord.ReplaceAllString(s, "_") }

// wellFormedConsumer is the CONTROL source used throughout the hard-failure
// lattice: a consumer the generator must succeed on. It declares two tagged
// fields and references one of them.
const wellFormedConsumer = `package consumer

type Classification struct {
	Risk       string   ` + "`json:\"risk\"`" + `
	Components []string ` + "`json:\"components,omitempty\"`" + `
}

func use(c *Classification) string { return c.Risk }
`

// ─── the hard-failure rule ───────────────────────────────────────────────────

// A generator that degrades when it cannot see a consumer is the hand-list bug
// with an AST in front of it. Every way of not seeing a consumer must produce
// an error AND a zero ReadSet — never a partial one.
//
// Each row pairs the broken consumer with the well-formed CONTROL in the SAME
// call. That is what makes "must NEVER return a partial ReadSet" testable:
// the control alone would yield a non-empty set, so a generator that skipped
// the broken entry and returned the control's fields would be caught by the
// emptiness assertion rather than only by the error assertion.
func TestSeal_GenerateReadSet_FailsHardAndNeverPartially(t *testing.T) {
	defer red(t)

	// CONTROL first, on its own: the well-formed world must succeed, or every
	// row below is vacuously satisfied by a generator that always errors.
	t.Run("CONTROL_wellformed_world_succeeds", func(t *testing.T) {
		defer red(t)
		rs, err := GenerateReadSet(synthConsumers(t, map[string]string{"alpha": wellFormedConsumer}))
		if err != nil {
			t.Fatalf("the well-formed control errored: %v", err)
		}
		if len(rs.Fields) != 2 {
			t.Fatalf("control read set = %+v, want risk and components", rs.Fields)
		}
	})

	broken := []struct {
		name string
		// src is written for the "beta" consumer; empty means the directory is
		// not created at all.
		src string
		// typeName overrides "Classification" for the beta consumer.
		typeName string
		why      string
	}{
		{
			name: "missing_directory",
			src:  "",
			why:  "a consumer directory that is not there. The omission would be invisible and the emitter would drop a field a consumer still reads.",
		},
		{
			name: "unparseable_source",
			src:  "package consumer\n\ntype Classification struct {\n\tRisk string `json:\"risk\"`\n",
			why:  "source go/parser cannot parse. Half a file is not half a read set.",
		},
		{
			name:     "type_not_declared",
			src:      wellFormedConsumer,
			typeName: "RenamedClassification",
			why:      "TypeName is taken as data precisely so a RENAME in a frozen consumer produces a hard failure and not an empty read set.",
		},
		{
			name: "untagged_field",
			src: `package consumer

type Classification struct {
	Risk         string ` + "`json:\"risk\"`" + `
	ReviewerArgs []string
}
`,
			why: "an untagged Go field marshals under its Go NAME, which is a different wire key. Guessing between the two is the silent mismatch this generator exists to prevent.",
		},
		{
			name: "dash_tagged_field",
			src: `package consumer

type Classification struct {
	Risk     string ` + "`json:\"risk\"`" + `
	Internal string ` + "`json:\"-\"`" + `
}
`,
			why: `a "-" tag has no wire key at all.`,
		},
		{
			name: "malformed_tag",
			src: `package consumer

type Classification struct {
	Risk  string ` + "`json:\"risk\"`" + `
	Other string ` + "`json:`" + `
}
`,
			why: "a malformed struct tag.",
		},
	}

	for _, b := range broken {
		b := b
		t.Run(b.name, func(t *testing.T) {
			defer red(t)
			files := map[string]string{"alpha": wellFormedConsumer}
			if b.src != "" {
				files["beta"] = b.src
			}
			cs := synthConsumers(t, files)
			if b.src == "" {
				// The directory was never created; name one that is not there.
				cs = append(cs, ConsumerSource{
					Name:     "beta",
					Dir:      filepath.Join("testdata", "readset-gen", sanitize(t.Name()), "beta"),
					TypeName: "Classification",
				})
			}
			if b.typeName != "" {
				for i := range cs {
					if cs[i].Name == "beta" {
						cs[i].TypeName = b.typeName
					}
				}
			}

			rs, err := GenerateReadSet(cs)
			if err == nil {
				t.Fatalf("GenerateReadSet succeeded with a broken consumer (%s): %s\ngot %+v", b.name, b.why, rs)
			}
			if len(rs.Fields) != 0 || len(rs.Consumers) != 0 {
				t.Errorf("GenerateReadSet returned a PARTIAL read set alongside its error: %+v.\nA partial set is worse than no set: callers that only check the error would be fine, and callers that use the value would silently emit less than a consumer reads.", rs)
			}
		})
	}
}

// ─── the real consumers ──────────────────────────────────────────────────────

// wantReadSet is the baseline expectation stated in readset.go, with the
// attribution the scaffold CORRECTED: risk and components are declared by
// cmd/gates as well as cmd/iterate, not by iterate alone.
//
// Getting the union right and the attribution wrong is survivable; getting the
// union wrong is not. Both are sealed, because the attribution is what makes a
// regression report actionable.
var wantReadSet = map[string][]string{
	"changed_files[].path": {"gates"},
	"components":           {"gates", "iterate"},
	"recheck_min_severity": {"iterate"},
	"reviewer_args":        {"iterate"},
	"risk":                 {"gates", "iterate"},
}

// The generated set over the frozen consumers.
//
// If a future run produces a sixth field, someone edited a frozen consumer and
// the emitter must be re-checked before that edit ships. That is what this row
// is for: it is not pinning a number, it is making a consumer edit visible on
// the producer's side, which is the only place the emitter can be fixed.
func TestSeal_GenerateReadSet_TheFrozenConsumersUnionAndAttribution(t *testing.T) {
	defer red(t)

	rs, err := GenerateReadSet(frozenConsumers)
	if err != nil {
		t.Fatalf("GenerateReadSet over the frozen consumers errored: %v", err)
	}

	if !sameStrings(rs.Consumers, []string{"gates", "iterate"}) {
		t.Errorf("Consumers = %v, want [gates iterate] — a short Consumers list is a read set generated against an incomplete world", rs.Consumers)
	}

	var gotPaths []string
	for _, f := range rs.Fields {
		gotPaths = append(gotPaths, f.JSONPath)
	}
	if !sameStrings(gotPaths, sortedCopy(gotPaths)) {
		t.Errorf("Fields is not sorted by JSONPath: %v — the output is meant to be committed and diffed", gotPaths)
	}

	var wantPaths []string
	for p := range wantReadSet {
		wantPaths = append(wantPaths, p)
	}
	wantPaths = sortedCopy(wantPaths)
	if !sameStrings(gotPaths, wantPaths) {
		t.Errorf("read set = %v, want %v.\n"+
			"A field that APPEARED means a frozen consumer grew a dependency the v1 emitter may not fill.\n"+
			"A field that VANISHED is the recheck_min_severity failure repeating: iterate's floorFor would fall back to \"high\" and skip every MEDIUM finding.",
			gotPaths, wantPaths)
	}

	byPath := map[string]ReadField{}
	for _, f := range rs.Fields {
		byPath[f.JSONPath] = f
	}
	for path, wantConsumers := range wantReadSet {
		f, ok := byPath[path]
		if !ok {
			continue // already reported above
		}
		if !sameStrings(f.Consumers, wantConsumers) {
			t.Errorf("%s: Consumers = %v, want %v", path, f.Consumers, wantConsumers)
		}
		if f.Kind != ReadKindDeclared {
			t.Errorf("%s: Kind = %d, want ReadKindDeclared — Referenced is recorded in Sites and in the Referenced flag, so a field appears exactly once", path, int(f.Kind))
		}
		if len(f.Sites) == 0 {
			t.Errorf("%s: no Sites — the citation is the whole point; prose citations go stale and this effort has already caught several", path)
		}
	}

	// Nested structs are descended, and the descent must not OVERSTATE the
	// obligation: cmd/gates declares FileClass with `path` only, while the
	// producer's FileClass carries path, risk and rules (main.go:159-163).
	for _, over := range []string{"changed_files[].risk", "changed_files[].rules", "changed_files"} {
		if _, ok := byPath[over]; ok {
			t.Errorf("read set contains %q — no consumer declares it, and reporting it would overstate the emitter's obligation", over)
		}
	}
	// And these are the fields the earlier informal list wrongly named.
	for _, phantom := range []string{"skills", "config_path", "panel.reasons", "panel", "classified_at"} {
		if _, ok := byPath[phantom]; ok {
			t.Errorf("read set contains %q — no consumer declares it. That name comes from the informal list the generator exists to replace.", phantom)
		}
	}
}

// The generated citations must be TRUE, not merely present.
//
// This is the seal that makes "generated, never hand-typed" mean something: for
// every declared site the generator emits, the named file at the named line
// must actually contain that field's JSON tag. A generator that produced
// plausible line numbers would pass every other row in this file.
func TestSeal_GenerateReadSet_CitationsResolveToRealLines(t *testing.T) {
	defer red(t)

	rs, err := GenerateReadSet(frozenConsumers)
	if err != nil {
		t.Fatalf("GenerateReadSet errored: %v", err)
	}

	cache := map[string][]string{}
	readLines := func(t *testing.T, repoRel string) []string {
		t.Helper()
		if l, ok := cache[repoRel]; ok {
			return l
		}
		// Sites are repo-relative ("cmd/iterate/main.go"); the test runs in
		// cmd/classify.
		p := filepath.Join("..", "..", repoRel)
		data, err := os.ReadFile(p) // #nosec G304 -- a path this package's own generator produced
		if err != nil {
			t.Fatalf("site names %q, which does not resolve to a file (%s): %v", repoRel, p, err)
		}
		l := strings.Split(string(data), "\n")
		cache[repoRel] = l
		return l
	}

	checkedDeclared := 0
	for _, f := range rs.Fields {
		leaf := f.JSONPath
		if i := strings.LastIndex(leaf, "."); i >= 0 {
			leaf = leaf[i+1:]
		}
		for _, s := range f.Sites {
			if !strings.HasPrefix(s.File, "cmd/") {
				t.Errorf("%s: site File = %q, want a repo-relative path like cmd/gates/main.go", f.JSONPath, s.File)
				continue
			}
			lines := readLines(t, s.File)
			if s.Line < 1 || s.Line > len(lines) {
				t.Errorf("%s: site %s:%d is outside the file (%d lines)", f.JSONPath, s.File, s.Line, len(lines))
				continue
			}
			line := lines[s.Line-1]
			switch s.What {
			case "declared":
				if !strings.Contains(line, `json:"`+leaf) {
					t.Errorf("%s: declared site %s:%d does not carry the tag for %q. The line is:\n\t%s", f.JSONPath, s.File, s.Line, leaf, strings.TrimSpace(line))
					continue
				}
				checkedDeclared++
			case "referenced":
				// The selector uses the Go field name, which the generator
				// knows and the seal does not; assert only that the line is
				// non-blank and not the declaration itself.
				if strings.TrimSpace(line) == "" {
					t.Errorf("%s: referenced site %s:%d is a blank line", f.JSONPath, s.File, s.Line)
				}
			default:
				t.Errorf("%s: site What = %q, want \"declared\" or \"referenced\"", f.JSONPath, s.What)
			}
		}
	}
	if checkedDeclared == 0 {
		t.Error("no declared site was verified — this seal compared nothing")
	}
}

// Determinism is contractual: the output is meant to be committed and diffed,
// and a generator whose Sites order wobbled would produce a diff every run and
// train everyone to ignore it.
func TestSeal_GenerateReadSet_IsDeterministic(t *testing.T) {
	defer red(t)
	a, err := GenerateReadSet(frozenConsumers)
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	b, err := GenerateReadSet(frozenConsumers)
	if err != nil {
		t.Fatalf("errored on the second run: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two runs over identical sources produced different ReadSets, including Sites order:\n%+v\n%+v", a, b)
	}
}

// ─── checking the DECLARED-normative ruling ──────────────────────────────────

// The scaffold rules DECLARED normative for emitter totality and REFERENCED
// advisory. I checked the ruling rather than adopting it, and it is right — but
// the real consumers CANNOT demonstrate it: at baseline every field cmd/gates
// and cmd/iterate declare is also referenced, so the two definitions coincide
// and a body author could implement REFERENCED-only and pass every row above.
//
// So the ruling is sealed structurally, on a synthetic consumer that declares a
// field it never reaches. Two obligations, opposite directions:
//
//   - a DECLARED-but-unreferenced field IS in the read set, with Referenced
//     false. It is what survives the consumer's unmarshal-and-marshal-back
//     cycle, so it is what the consumer preserves for whoever reads the file
//     next — and it is one consumer edit away from being referenced, with no
//     producer change to notice.
//   - a REFERENCED-but-undeclared selector is NOT in the read set. The emitter's
//     obligation comes from the struct, not from an expression that happens to
//     look like a field access.
func TestSeal_DeclaredIsNormative_ReferencedIsAdvisory(t *testing.T) {
	defer red(t)

	src := `package consumer

type Classification struct {
	Risk          string   ` + "`json:\"risk\"`" + `
	PreservedOnly []string ` + "`json:\"preserved_only,omitempty\"`" + `
}

// Risk is reached; PreservedOnly never is. It is still carried through every
// unmarshal-and-marshal-back cycle this consumer performs.
func use(c *Classification) string {
	if c.NotAField != "" {
		return c.NotAField
	}
	return c.Risk
}
`
	rs, err := GenerateReadSet(synthConsumers(t, map[string]string{"synthetic": src}))
	if err != nil {
		t.Fatalf("GenerateReadSet errored: %v", err)
	}

	byPath := map[string]ReadField{}
	for _, f := range rs.Fields {
		byPath[f.JSONPath] = f
	}

	pres, ok := byPath["preserved_only"]
	if !ok {
		t.Fatalf("a DECLARED but unreferenced field is missing from the read set (%v).\n"+
			"DECLARED is normative for emitter totality: the field survives the consumer's unmarshal/marshal cycle, and it is one consumer edit away from being read with no producer change to notice.\n"+
			"An emitter built to the REFERENCED set only would be exactly the hand-list failure with an AST in front of it.", pathsOf(rs))
	}
	if pres.Referenced {
		t.Error("preserved_only is marked Referenced, but no selector reaches it")
	}
	if pres.Kind != ReadKindDeclared {
		t.Errorf("preserved_only Kind = %d, want ReadKindDeclared", int(pres.Kind))
	}
	// A field carried in the read set with no consumer and no citation is a
	// field that will not be attributed in a regression report and cannot be
	// checked against its source. Present-but-hollow is not present.
	if !sameStrings(pres.Consumers, []string{"synthetic"}) {
		t.Errorf("preserved_only Consumers = %v, want [synthetic] — an unreferenced field is still the consumer's obligation and must be attributed to it", pres.Consumers)
	}
	if len(pres.Sites) == 0 {
		t.Error("preserved_only has no Sites — an entry with no citation cannot be checked against the consumer's source")
	}

	risk, ok := byPath["risk"]
	if !ok {
		t.Fatal("CONTROL: risk is missing from the read set")
	}
	if !risk.Referenced {
		t.Error("risk is not marked Referenced, but a selector reaches it — the advisory flag is not being computed")
	}

	if _, bad := byPath["not_a_field"]; bad {
		t.Error("a selector that matches no declared field produced a read-set entry")
	}
	if _, bad := byPath["NotAField"]; bad {
		t.Error("a selector that matches no declared field produced a read-set entry under its Go name")
	}
}

func pathsOf(rs ReadSet) []string {
	var out []string
	for _, f := range rs.Fields {
		out = append(out, f.JSONPath)
	}
	return out
}

// ─── EmitterCovers ───────────────────────────────────────────────────────────

// The v1 emission covers the whole generated read set — on an input that makes
// every read field non-zero.
//
// The omitempty rule is why that qualification matters and is sealed here as
// its own row: for the consumer, an absent key and a zero value are the same
// bytes, so an elided key is MISSING. The docs-only fixture emits no
// `components` and no `reviewer_args`-visible components, and EmitterCovers
// must say so rather than quietly counting the field as covered.
func TestSeal_EmitterCovers_ReadSetOverRealV1Emissions(t *testing.T) {
	defer red(t)

	rs, err := GenerateReadSet(frozenConsumers)
	if err != nil {
		t.Fatalf("GenerateReadSet errored: %v", err)
	}

	// The wallet fixture populates every field in the read set.
	wallet := sealFixtures()[0]
	var buf bytes.Buffer
	if err := EmitV1(&buf, wallet.classification(t)); err != nil {
		t.Fatalf("EmitV1 errored: %v", err)
	}
	missing, err := EmitterCovers(rs, buf.Bytes())
	if err != nil {
		t.Fatalf("EmitterCovers errored: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("the v1 emission does not cover %v of the generated read set.\nEmission:\n%s", missing, buf.String())
	}

	// CONTROL for non-vacuity: an emission with an elided components key must
	// be reported, or EmitterCovers is returning nil unconditionally.
	elided := &Classification{Risk: "low", Panel: Panel{Required: true, Seats: 1, Reduced: true}, RecheckMinSeverity: "high"}
	var buf2 bytes.Buffer
	if err := EmitV1(&buf2, elided); err != nil {
		t.Fatalf("EmitV1 errored: %v", err)
	}
	missing2, err := EmitterCovers(rs, buf2.Bytes())
	if err != nil {
		t.Fatalf("EmitterCovers errored: %v", err)
	}
	if len(containsAll(missing2, []string{"components", "reviewer_args", "changed_files[].path"})) > 0 {
		t.Errorf("EmitterCovers reported %v for an emission that elides components, reviewer_args and changed_files.\n"+
			"An omitempty-elided key must count as MISSING: for the consumer, an absent key and a zero value are the same bytes, and the emitter's totality claim is about bytes.\nEmission:\n%s",
			missing2, buf2.String())
	}
	if len(containsAll(missing2, []string{"risk"})) == 0 {
		t.Error("EmitterCovers reported risk as missing from an emission that carries it")
	}
}

// ─── SemanticEquivalentV1: the guards ────────────────────────────────────────

// The differential's error arms. A differential over an empty domain would
// report Equivalent: true having compared nothing, which is the vacuous pass
// this whole design keeps naming.
func TestSeal_SemanticEquivalentV1_Guards(t *testing.T) {
	defer red(t)

	rs := ReadSet{
		Fields:    []ReadField{{JSONPath: "risk", Consumers: []string{"x"}, Kind: ReadKindDeclared}},
		Consumers: []string{"x"},
	}
	doc := []byte(`{"risk":"critical"}`)

	// CONTROL: the well-formed call succeeds and compares something.
	got, err := SemanticEquivalentV1(doc, doc, rs, nil)
	if err != nil {
		t.Fatalf("CONTROL errored: %v", err)
	}
	if !got.Equivalent {
		t.Errorf("CONTROL: identical documents are not equivalent: %+v", got)
	}
	if len(got.Compared) == 0 {
		t.Error("CONTROL: Compared is empty on a successful comparison — a pass that looked at nothing is not a pass")
	}

	if _, err := SemanticEquivalentV1(doc, doc, ReadSet{}, nil); err == nil {
		t.Error("an EMPTY read set must error — otherwise the differential reports Equivalent: true having compared nothing")
	}
	if _, err := SemanticEquivalentV1([]byte(`{`), doc, rs, nil); err == nil {
		t.Error("an unparseable OLD document must error")
	}
	if _, err := SemanticEquivalentV1(doc, []byte(`nope`), rs, nil); err == nil {
		t.Error("an unparseable NEW document must error")
	}
	if _, err := SemanticEquivalentV1(doc, doc, rs, []string{"no_such_path"}); err == nil {
		t.Error("a volatile name that is not in the read set must error — a typo'd exclusion would silently drop nothing and be invisible")
	}
}

// Absence is a value: a path in the read set that is missing from EITHER
// document is a divergence, not a skip.
//
// The failure this closes: "all keys present in both" as the domain would let a
// field the new binary stopped emitting pass unnoticed — which is precisely the
// v1 emitter losing recheck_min_severity, arriving through the test instead of
// through the code.
func TestSeal_SemanticEquivalentV1_AbsenceIsADivergence(t *testing.T) {
	defer red(t)

	rs := ReadSet{
		Fields: []ReadField{
			{JSONPath: "risk", Kind: ReadKindDeclared},
			{JSONPath: "recheck_min_severity", Kind: ReadKindDeclared},
		},
		Consumers: []string{"iterate"},
	}
	old := []byte(`{"risk":"critical","recheck_min_severity":"medium"}`)
	dropped := []byte(`{"risk":"critical"}`)

	res, err := SemanticEquivalentV1(old, dropped, rs, nil)
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if res.Equivalent {
		t.Error("a document that DROPPED recheck_min_severity was judged equivalent. Absence is a value: iterate's floorFor would return its \"high\" fallback and skip every MEDIUM finding.")
	}
	if !sameStrings(sortedCopy(res.Compared), []string{"recheck_min_severity", "risk"}) {
		t.Errorf("Compared = %v, want both read-set paths — a path absent from one side is compared, not skipped", res.Compared)
	}
	found := false
	for _, d := range res.Divergences {
		if d.JSONPath == "recheck_min_severity" {
			found = true
		}
	}
	if !found {
		t.Errorf("Divergences %v does not name recheck_min_severity", res.Divergences)
	}

	// The other direction: a path absent from the OLD document is equally a
	// divergence, not a "harmless addition".
	res, err = SemanticEquivalentV1(dropped, old, rs, nil)
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if res.Equivalent {
		t.Error("a path absent from the OLD document was judged equivalent")
	}

	// "Absence is a value" resolves the other half of the rule: a path both
	// documents omit is COMPARED and AGREES. It is not a divergence and it is
	// not a skip. Without this row the differential could not pass on any real
	// fixture — the docs-only classification omits components from both sides,
	// because the v1 tag is `components,omitempty`.
	bothAbsent := []byte(`{"risk":"low"}`)
	res, err = SemanticEquivalentV1(bothAbsent, bothAbsent, rs, nil)
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if !res.Equivalent {
		t.Errorf("two documents that BOTH omit recheck_min_severity were judged divergent: %+v. Absence is a value, and two absences are the same value; a rule that failed here could never pass on the docs-only fixture, whose components key is elided by `components,omitempty` on both sides.", res.Divergences)
	}
	if len(containsAll(res.Compared, []string{"recheck_min_severity"})) > 0 {
		t.Errorf("Compared = %v — a path absent from both documents is still COMPARED, not skipped; otherwise the domain shrinks silently", res.Compared)
	}

	// CONTROL: a key OUTSIDE the read set differing is NOT a divergence. The
	// domain is exactly rs, not "all keys in old" — which would fail on
	// harmless additions.
	res, err = SemanticEquivalentV1(
		[]byte(`{"risk":"critical","recheck_min_severity":"medium","skills":["a"]}`),
		[]byte(`{"risk":"critical","recheck_min_severity":"medium","skills":["b"],"new_field":1}`),
		rs, nil)
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if !res.Equivalent {
		t.Errorf("CONTROL: a difference outside the read set was treated as a divergence: %+v", res.Divergences)
	}
}

// Comparison is semantic, not textual, and the result must SAY what it looked
// at: Compared is the read set minus volatile, and Excluded names what was
// skipped, so the exclusion is visible in the output rather than implied by its
// absence.
func TestSeal_SemanticEquivalentV1_SemanticsAndNonVacuity(t *testing.T) {
	defer red(t)

	rs := ReadSet{
		Fields: []ReadField{
			{JSONPath: "components", Kind: ReadKindDeclared},
			{JSONPath: "seats", Kind: ReadKindDeclared},
			{JSONPath: "stamp", Kind: ReadKindDeclared},
		},
		Consumers: []string{"x"},
	}

	// Numbers compare numerically, not textually.
	res, err := SemanticEquivalentV1(
		[]byte(`{"components":["wallet"],"seats":5,"stamp":"a"}`),
		[]byte(`{"components":["wallet"],"seats":5.0,"stamp":"b"}`),
		rs, []string{"stamp"})
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if !res.Equivalent {
		t.Errorf("5 and 5.0 were judged different: %+v — JSON numbers compare numerically", res.Divergences)
	}
	if !sameStrings(sortedCopy(res.Compared), []string{"components", "seats"}) {
		t.Errorf("Compared = %v, want exactly the read set MINUS volatile", res.Compared)
	}
	if !sameStrings(res.Excluded, []string{"stamp"}) {
		t.Errorf("Excluded = %v, want [stamp] — the exclusion must be visible in the output", res.Excluded)
	}

	// Slices compare element-wise IN ORDER. The producer's ordering is already
	// deterministic (components via sortedKeys at main.go:737, risk_reasons in
	// fixed tier order at main.go:741), so a reordering is a real change.
	res, err = SemanticEquivalentV1(
		[]byte(`{"components":["bet-settlement","wallet"],"seats":5,"stamp":"a"}`),
		[]byte(`{"components":["wallet","bet-settlement"],"seats":5,"stamp":"a"}`),
		rs, []string{"stamp"})
	if err != nil {
		t.Fatalf("errored: %v", err)
	}
	if res.Equivalent {
		t.Error("a reordered components array was judged equivalent — slices compare element-wise in order")
	}
}

// DISPUTE FOR P4 — recorded as a seal so the contradiction cannot be settled by
// accident.
//
// SemanticEquivalentV1's contract says it "errors if [...] volatile names a
// path not in rs". volatileV1Fields is exactly ["classified_at"]. But
// classified_at is declared by NO consumer, so the generated read set never
// contains it. The invocation the design describes —
//
//	SemanticEquivalentV1(old, new, GenerateReadSet(frozenConsumers), volatileV1Fields)
//
// therefore errors by the contract's own letter. The two readings are:
//
//	(a) the guard is right and the domain is right, and the differential is
//	    simply called with an EMPTY volatile list, because a read set that never
//	    contains a clock has nothing to exclude. volatileV1Fields then documents
//	    the exclusion for the byte-level snapshot rather than for this function.
//	(b) the guard is too strong and a volatile name outside rs should be a
//	    tolerated no-op recorded in Excluded.
//
// I am sealing (a): the guard has real work to do — it catches a typo'd or
// stale exclusion, which is a change to a wire-contract-grade list — and (b)
// would make an exclusion that silences nothing indistinguishable from one that
// silences a field. This row pins the letter of the contract. If P4 rules for
// (b), this is the one row that flips, and the differential rows below do not
// move, because they already pass volatile explicitly.
func TestSeal_Dispute_VolatileFieldIsNotInTheGeneratedReadSet(t *testing.T) {
	defer red(t)

	rs, err := GenerateReadSet(frozenConsumers)
	if err != nil {
		t.Fatalf("GenerateReadSet errored: %v", err)
	}
	for _, f := range rs.Fields {
		if f.JSONPath == "classified_at" {
			t.Fatalf("classified_at is now in the generated read set — a frozen consumer started declaring the clock, and the volatile exclusion is now live. Re-read this dispute; it may have resolved itself.")
		}
	}
	if !sameStrings(volatileV1Fields, []string{"classified_at"}) {
		t.Errorf("volatileV1Fields = %v. Adding a field here declares that the two binaries may legitimately disagree about it — a much stronger claim than \"this test is flaky\" — and needs the same review as a schema change.", volatileV1Fields)
	}

	f := sealFixtures()[0]
	old := pinnedV1(t, f)
	var buf bytes.Buffer
	if err := EmitV1(&buf, f.classification(t)); err != nil {
		t.Fatalf("EmitV1 errored: %v", err)
	}

	if _, err := SemanticEquivalentV1(old, buf.Bytes(), rs, volatileV1Fields); err == nil {
		t.Error("SemanticEquivalentV1 accepted volatileV1Fields against the generated read set. " +
			"By the contract's letter that must error, because classified_at is not in rs. " +
			"See the DISPUTE note above: if P4 ruled for reading (b), change this row and say so in the commit.")
	}
}

// ─── the v1 differential against the pinned binary ───────────────────────────

// Identical inputs, two producers, semantic equivalence over the GENERATED read
// set. This is the differential the plan asks for, and its non-vacuity guard is
// the Compared list, which the scaffold explicitly demands a seal assert:
// "a seal that only asserts Equivalent is a vacuous seal".
func TestSeal_V1Differential_AgainstThePinnedBinary(t *testing.T) {
	defer red(t)

	rs, err := GenerateReadSet(frozenConsumers)
	if err != nil {
		t.Fatalf("GenerateReadSet errored: %v", err)
	}
	wantPaths := sortedCopy(pathsOf(rs))
	if len(wantPaths) == 0 {
		t.Fatal("the generated read set is empty; the differential would compare nothing")
	}

	for _, f := range sealFixtures() {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			defer red(t)
			old := pinnedV1(t, f)
			if len(old) == 0 {
				t.Fatal("the pinned binary produced no output; there is nothing to compare against")
			}
			var buf bytes.Buffer
			if err := EmitV1(&buf, f.classification(t)); err != nil {
				t.Fatalf("EmitV1 errored: %v", err)
			}

			// volatile is empty: classified_at is not in the read set, so
			// there is nothing to exclude. See the dispute above.
			res, err := SemanticEquivalentV1(old, buf.Bytes(), rs, nil)
			if err != nil {
				t.Fatalf("SemanticEquivalentV1 errored: %v", err)
			}
			if !res.Equivalent {
				t.Errorf("the new v1 emission diverges from the pinned binary: %+v\npinned:\n%s\nnew:\n%s", res.Divergences, old, buf.String())
			}
			if !sameStrings(sortedCopy(res.Compared), wantPaths) {
				t.Errorf("Compared = %v, want the whole read set %v. A differential that happened to look at nothing would report Equivalent: true, so the domain is asserted, not the verdict alone.", sortedCopy(res.Compared), wantPaths)
			}
		})
	}
}

// The stronger claim EmitV1's contract actually makes: BYTE-IDENTICAL to the
// pinned binary, minus nothing and plus nothing — same struct, same field
// order, same omitempty behaviour, two-space indent, trailing newline.
//
// Only classified_at is normalised away, and only its VALUE: the key must still
// be in the same place, so a body that dropped it is caught.
func TestSeal_V1BytesAreIdenticalToThePinnedBinary(t *testing.T) {
	defer red(t)

	for _, f := range sealFixtures() {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			defer red(t)
			old := normaliseClassifiedAt(t, pinnedV1(t, f))
			if len(old) == 0 {
				t.Fatal("the pinned binary produced no output; there is nothing to compare against")
			}
			var buf bytes.Buffer
			if err := EmitV1(&buf, f.classification(t)); err != nil {
				t.Fatalf("EmitV1 errored: %v", err)
			}
			got := normaliseClassifiedAt(t, buf.Bytes())
			if !bytes.Equal(old, got) {
				t.Errorf("v1 bytes differ from the pinned producer (%s).\nAny change here is a wire break dressed as a refactor.\npinned:\n%s\nnew:\n%s", f.Why, old, got)
			}
		})
	}
}

var classifiedAtRE = regexp.MustCompile(`"classified_at": "[^"]*"`)

// normaliseClassifiedAt replaces the volatile timestamp's VALUE and asserts the
// key was there. Byte-identity was impossible as written precisely because of
// this field; normalising the value rather than deleting the line keeps the
// key's presence and position under test.
func normaliseClassifiedAt(t *testing.T, b []byte) []byte {
	t.Helper()
	if !classifiedAtRE.Match(b) {
		t.Fatalf("no classified_at in the v1 emission — the pinned binary stamps it at main.go:274 and the legacy shape declares it at main.go:143:\n%s", b)
	}
	return classifiedAtRE.ReplaceAll(b, []byte(`"classified_at": "<VOLATILE>"`))
}

// The v1 config_scaffold triple.
//
// PRODUCTION REACHABILITY, stated per row because one of the three is NOT
// producible and that fact is itself the seal:
//   - OMITTED: every run against a reviewed config. The v1 tag is
//     `config_scaffold,omitempty` (main.go:141).
//   - TRUE: any run against a `classify init` config. testdata/scaffold-config.json
//     IS that generator's output.
//   - PRESENT-AND-FALSE: NOT PRODUCIBLE by this producer, ever, because
//     omitempty elides it. That is exactly why DesugarConfigScaffold exists:
//     the absent-means-false problem is closed at the CONSUMER by a named total
//     rule, not by mutating the legacy shape. Removing omitempty here would
//     change v1 bytes for every non-scaffold run.
func TestSeal_V1_ConfigScaffoldOmitAndTrue(t *testing.T) {
	defer red(t)

	for _, f := range sealFixtures() {
		cls := f.classification(t)
		var buf bytes.Buffer
		if err := EmitV1(&buf, cls); err != nil {
			t.Fatalf("%s: EmitV1 errored: %v", f.Name, err)
		}
		present := hasKey(t, buf.Bytes(), "config_scaffold")
		if cls.ConfigScaffold && !present {
			t.Errorf("%s: config_scaffold is true but the key is absent from the v1 emission", f.Name)
		}
		if !cls.ConfigScaffold && present {
			t.Errorf("%s: config_scaffold is false and the key is PRESENT. v1 keeps `config_scaffold,omitempty` (main.go:141) deliberately; emitting it would change v1 bytes for every non-scaffold run.", f.Name)
		}
		// And the pinned binary agrees, which is what makes the claim about
		// production rather than about this test's expectations.
		if got := hasKey(t, pinnedV1(t, f), "config_scaffold"); got != present {
			t.Errorf("%s: the pinned binary %s the key and the new emission %s it", f.Name, presence(got), presence(present))
		}
	}
}

func presence(b bool) string {
	if b {
		return "emits"
	}
	return "omits"
}

// ─── goldens ─────────────────────────────────────────────────────────────────

// T10: golden fixtures per contract version.
//
// The v1 goldens are the pinned binary's own bytes, checked in with the
// timestamp normalised, so the committed file is the legacy wire as it actually
// is rather than as anyone remembers it. The v2 goldens are the deterministic
// serialization snapshot of the new emission.
//
// Regenerating a golden is not a fix. If a golden diff appears, the question is
// which of the two wires changed and who reads it.
func TestSeal_Goldens_BothContracts(t *testing.T) {
	defer red(t)

	for _, f := range sealFixtures() {
		f := f
		t.Run(f.Name+"/v1", func(t *testing.T) {
			defer red(t)
			var buf bytes.Buffer
			if err := EmitV1(&buf, f.classification(t)); err != nil {
				t.Fatalf("EmitV1 errored: %v", err)
			}
			compareGolden(t, filepath.Join("testdata", "golden", "v1-"+f.Name+".json"), normaliseClassifiedAt(t, buf.Bytes()))
		})
		t.Run(f.Name+"/v2", func(t *testing.T) {
			defer red(t)
			withHooks(t, nil, fakeDigests{
				config: "0000000000000000000000000000000000000000000000000000000000000001",
				diff:   "0000000000000000000000000000000000000000000000000000000000000002",
			}, nil)
			var buf bytes.Buffer
			if err := EmitV2(&buf, f.classification(t)); err != nil {
				t.Fatalf("EmitV2 errored: %v", err)
			}
			compareGolden(t, filepath.Join("testdata", "golden", "v2-"+f.Name+".json"), buf.Bytes())
		})
	}
}

func compareGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(path) // #nosec G304 -- a path this test built from a fixture name
	if err != nil {
		t.Fatalf("golden %s is missing: %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("golden %s differs.\nwant:\n%s\ngot:\n%s", path, want, got)
	}
}

// Rollback mid-run, on the producer's side: the same classification emitted at
// v2 and then at v1 must yield the same v1 bytes as a v1-only run. Emission
// carries no state between contracts, so a run that was rolled back from v2 to
// v1 mid-flight cannot produce a hybrid.
func TestSeal_RollbackMidRun_EmissionCarriesNoStateBetweenContracts(t *testing.T) {
	defer red(t)

	f := sealFixtures()[0]
	cls := f.classification(t)

	var v1a bytes.Buffer
	if err := EmitV1(&v1a, cls); err != nil {
		t.Fatalf("EmitV1 errored: %v", err)
	}

	withHooks(t, nil, fakeDigests{config: strings.Repeat("1", 64), diff: strings.Repeat("2", 64)}, nil)
	var v2 bytes.Buffer
	if err := EmitV2(&v2, cls); err != nil {
		t.Fatalf("EmitV2 errored: %v", err)
	}

	var v1b bytes.Buffer
	if err := EmitV1(&v1b, cls); err != nil {
		t.Fatalf("EmitV1 after EmitV2 errored: %v", err)
	}
	if !bytes.Equal(normaliseClassifiedAt(t, v1a.Bytes()), normaliseClassifiedAt(t, v1b.Bytes())) {
		t.Errorf("the v1 emission changed after a v2 emission of the same classification.\nbefore:\n%s\nafter:\n%s", v1a.String(), v1b.String())
	}
	// And BuildV2 must not have mutated the classification it projected.
	var v1c bytes.Buffer
	if err := EmitV1(&v1c, cls); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(normaliseClassifiedAt(t, v1b.Bytes()), normaliseClassifiedAt(t, v1c.Bytes())) {
		t.Error("repeated v1 emissions of one classification are not stable")
	}
}

// ─── the sidecar survives; the v1 projection does not ────────────────────────

// The property the separate-file decision actually rests on.
//
// The design's §3.3 says "a differential fixture runs classify→gates→iterate
// and asserts BOTH projections survive every rewrite". That claim is FALSE for
// the v1 projection, measured in this worktree, and I have not written the
// fixture as specified. What is sealed is:
//
//   - the SIDECAR survives, byte for byte, because nothing else writes that
//     file. This is real and it is the whole reason for the separate file.
//   - the v1 loss is RECORDED, not asserted away, and the recording is
//     non-vacuous.
//
// P4 CORRECTION (adjudicate(B1)): the non-vacuity was originally claimed to
// follow from v1KeysLost being non-empty. It does not — a body that executes
// nothing returns "survived" trivially AND can return a non-empty list it never
// measured, and I confirmed by construction that such a stub passed this row as
// written. Non-vacuity now rests on the proof-of-execution block below, which
// requires the RUN-STATE itself to have been destroyed by the call. See there.
//
// §3.3's stated reason for the sidecar — "an unknown key would be silently
// dropped" — understates it. KNOWN keys are dropped too: cmd/gates declares
// Classification{risk, components, changed_files} with FileClass{path} only
// (gates/main.go:121-129), unmarshals the run-state into it and marshals it
// back (:1248, :1329).
func TestSeal_SidecarSurvives_AndRecordsTheV1Loss(t *testing.T) {
	defer red(t)

	dir := t.TempDir()
	runState := filepath.Join(dir, "run.json")
	seedRunState(t, runState)

	// The classification as the PINNED producer wrote it, captured before the
	// call. This is the baseline for the proof-of-execution block below, and it
	// is read from the run-state rather than taken from SidecarSurvives, so
	// nothing the function returns can influence it.
	before := classificationKeys(t, runState)
	if len(before) < 10 {
		t.Fatalf("the seeded classification has only %d keys (%v) — the fixture is not exercising the loss", len(before), before)
	}

	sidecar := V2SidecarPath(runState)
	sidecarBytes := []byte(`{"schema_version":1,"response":{"response_version":1,"computed_config_sha256":"` +
		strings.Repeat("a", 64) + `","computed_diff_sha256":"` + strings.Repeat("b", 64) + `","classification":{"contract_version":2}}}` + "\n")
	if err := os.WriteFile(sidecar, sidecarBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	survived, lost, err := SidecarSurvives(runState)
	if err != nil {
		t.Fatalf("SidecarSurvives errored: %v", err)
	}
	if !survived {
		t.Error("the v2 sidecar did NOT survive the frozen pipeline. It has exactly one writer, so any change to it means something else opened it — find out what.")
	}
	after, err := os.ReadFile(sidecar) // #nosec G304 -- a temp path this test created
	if err != nil {
		t.Fatalf("the sidecar is gone: %v", err)
	}
	if !bytes.Equal(after, sidecarBytes) {
		t.Errorf("the sidecar's bytes changed. Identical, not merely parseable, is the contract.\nbefore: %s\nafter:  %s", sidecarBytes, after)
	}

	if len(lost) == 0 {
		t.Fatal("v1KeysLost is empty. Either the pipeline did not run — in which case \"survived\" means nothing — or cmd/gates has been fixed, which is good news that this seal is required to notice.")
	}

	// PROOF OF EXECUTION — P4 AMENDMENT (adjudicate(B1)).
	//
	// This row previously rested its non-vacuity on `len(lost) != 0`, on the
	// stated grounds that "a SidecarSurvives that executed nothing would return
	// survived trivially". That reasoning does not hold, and I verified it does
	// not by building the stub: a body that stats the run-state, executes
	// NOTHING, and returns a statically-derived loss list — read off cmd/gates'
	// struct declaration, or simply hardcoded — passes every assertion above.
	// It reports survived=true precisely BECAUSE it ran nothing, so the
	// sidecar's bytes are trivially unchanged, and its non-empty `lost` clears
	// the guard. The seal certified a survival it never observed.
	//
	// So the proof is taken from the RUN-STATE, which only actually running the
	// frozen pipeline can mutate. This is deliberately an assertion about an
	// OBSERVABLE EFFECT and not about how the body finds the consumer binaries
	// (see the P4 ruling on dispute 4): pinning ../gates/gates here would
	// over-constrain the route while still not proving anything was executed,
	// whereas a destroyed run-state proves execution by whatever route.
	stateAfter := classificationKeys(t, runState)
	actuallyLost := containsAll(stateAfter, before)
	if len(actuallyLost) == 0 {
		t.Fatalf("SidecarSurvives reported lost=%v, but the run-state STILL CARRIES all %d classification keys (%v).\n"+
			"Nothing was executed, so \"survived\" is a claim about a rewrite that never happened. v1KeysLost must be MEASURED from this call, not derived from a struct declaration or hardcoded.\n"+
			"(If instead cmd/gates has genuinely been fixed, that is the good news this seal exists to catch — update recordedV1KeysLost and say which unit did it.)",
			lost, len(before), stateAfter)
	}
	if miss := containsAll(actuallyLost, lost); len(miss) > 0 {
		t.Errorf("SidecarSurvives reported %v as lost, but the run-state still carries %v after the call.\n"+
			"The reported loss must be what THIS call destroyed; a key named here that survived in the file is a list assembled from somewhere other than the measurement.", lost, miss)
	}

	for _, k := range recordedV1KeysLost {
		if len(containsAll(lost, []string{k})) > 0 {
			t.Errorf("v1KeysLost %v no longer contains %q.\n"+
				"If cmd/gates was widened to preserve it, that is the fix this project needs — update recordedV1KeysLost in the same commit and say which unit did it.\n"+
				"If it disappeared for any other reason, the measurement has stopped measuring.", lost, k)
		}
	}
}

// recordedV1KeysLost is a MEASUREMENT, taken in this worktree at seal time
// against the pinned classify, the tracked cmd/gates and the tracked
// cmd/iterate, on the wallet fixture. It is not a design intent and not an
// aspiration.
//
// Reproduction (15 classification keys before gates, 3 after):
//
//	classify -no-git -config testdata/example-monorepo.json -out run.json <wallet.diff>
//	gates -run-state run.json -config ../gates/testdata/example-gates.json -only nosuchgate
//	=> classification == {risk, components, changed_files:[{path}]}
//
// Two consequences that are not hypothetical, both observed:
//   - `iterate next -run-state run.json` then prints "Floor: high" where
//     classify wrote recheck_min_severity "medium" — floorFor
//     (iterate/main.go:270-275) finds nothing and returns its fallback, so every
//     MEDIUM finding is skipped on a critical money path.
//   - reviewer_args is gone, so iterate's round-1 argv (iterate/main.go:292)
//     carries no -risk and no -component and the panel runs at the generic
//     tier — the exact regression cmd/classify exists to prevent.
//
// changed_files[].risk and changed_files[].rules are destroyed too, inside a key
// that otherwise survives, which is why the loss is measured per JSON path and
// not per top-level key alone.
//
// This is tracked as its own unit against the frozen consumers and is out of
// B1's scope. It is recorded here so the day someone fixes it, a seal notices.
var recordedV1KeysLost = []string{
	"classified_at",
	"client_only",
	"config_path",
	"financial_paths_touched",
	"human_pr_gate",
	"migration",
	"panel",
	"recheck_min_severity",
	"reviewer_args",
	"risk_reasons",
	"server_surface",
	"skills",
}

// SidecarSurvives must ERROR when it cannot run, rather than reporting survival
// it did not observe.
func TestSeal_SidecarSurvives_ErrorsRatherThanReportingUnobservedSurvival(t *testing.T) {
	defer red(t)

	missing := filepath.Join(t.TempDir(), "not-there.json")
	survived, lost, err := SidecarSurvives(missing)
	if err == nil {
		t.Errorf("SidecarSurvives on a nonexistent run-state returned survived=%v lost=%v with no error — an unobserved survival is not a survival", survived, lost)
	}
	if survived {
		t.Error("SidecarSurvives reported survival for a run-state it could not read")
	}
}

// The RECORDED end-to-end measurement, run directly against the tracked
// consumer binaries so the fact does not depend on SidecarSurvives being
// implemented, and so the seal states what it measured rather than what it was
// told.
//
// GREEN TODAY, and that is the point: it pins a defect. It turns RED when
// cmd/gates stops destroying the classification, which is exactly when someone
// should be told that iterate's severity floor and the panel's tier have come
// back to life and the follow-up unit can close.
func TestSeal_Recorded_V1ProjectionDoesNotSurviveGates(t *testing.T) {
	t.Parallel()

	gatesBin := filepath.Join("..", "gates", "gates")
	gatesCfg := filepath.Join("..", "gates", "testdata", "example-gates.json")
	iterateBin := filepath.Join("..", "iterate", "iterate")
	for _, p := range []string{gatesBin, gatesCfg, iterateBin} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s is missing: %v — it is a tracked fixture and this measurement cannot run without it", p, err)
		}
	}

	dir := t.TempDir()
	runState := filepath.Join(dir, "run.json")
	seedRunState(t, runState)

	before := classificationKeys(t, runState)
	if len(before) < 10 {
		t.Fatalf("the seeded classification has only %d keys (%v) — the fixture is not exercising the loss", len(before), before)
	}

	cmd := exec.Command(gatesBin, "-run-state", runState, "-config", gatesCfg, "-only", "nosuchgate")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("gates failed: %v\n%s", err, out.String())
	}
	after := classificationKeys(t, runState)

	lost := containsAll(after, before)
	if len(lost) == 0 {
		t.Fatalf("cmd/gates preserved all %d classification keys.\n"+
			"THIS IS GOOD NEWS AND THIS SEAL IS SUPPOSED TO CATCH IT. The v1 projection now survives the frozen pipeline, §3.3's claim has become true, and the follow-up unit against the frozen consumers can close. Update recordedV1KeysLost and this row together.", len(before))
	}
	if miss := containsAll(lost, recordedV1KeysLost); len(miss) > 0 {
		t.Errorf("the measured loss no longer includes %v (lost: %v). The measurement has moved; re-derive it before trusting anything downstream of it.", miss, lost)
	}

	// The consequence, taken from the frozen consumer's own mouth rather than
	// inferred: iterate reports the fallback floor where classify wrote medium.
	icmd := exec.Command(iterateBin, "next", "-run-state", runState)
	var iout bytes.Buffer
	icmd.Stdout, icmd.Stderr = &iout, &iout
	_ = icmd.Run() // exit 1 == ITERATE, which is the expected verdict here
	if !strings.Contains(iout.String(), "Floor: high") {
		t.Errorf("cmd/iterate no longer reports the fallback floor after gates has run.\n"+
			"classify wrote recheck_min_severity=medium; if iterate now reports medium, the safety floor survives and every MEDIUM finding is being reviewed again. Re-read this seal.\n%s", iout.String())
	}
}

// seedRunState writes a complete, real run-state using the PINNED binary, so
// the measurement starts from bytes production actually produces.
func seedRunState(t *testing.T, runState string) {
	t.Helper()
	f := sealFixtures()[0]
	diffPath := filepath.Join(filepath.Dir(runState), "fixture.diff")
	if err := os.WriteFile(diffPath, []byte(f.diffText()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pinnedBinary, "-no-git", "-config", f.ConfigPath, "-task", "SMG-SEAL", "-out", runState, diffPath)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("seeding the run state with the pinned binary failed: %v\n%s", err, out.String())
	}
}

func classificationKeys(t *testing.T, runState string) []string {
	t.Helper()
	data, err := os.ReadFile(runState) // #nosec G304 -- a temp path this test created
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Classification map[string]json.RawMessage `json:"classification"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("run state is not valid JSON: %v", err)
	}
	out := make([]string, 0, len(state.Classification))
	for k := range state.Classification {
		out = append(out, k)
	}
	return sortedCopy(out)
}

// RECORDED FINDING against §3.3, stated as a seal because a finding nobody can
// run is a sentence in a document.
//
// The v2 envelope has NO changed_files, and cmd/gates READS changed_files
// (declared gates/main.go:124, read at :444-445). So the v2 sidecar alone
// cannot feed cmd/gates, and "migrate gates to v2" is not currently possible
// without extending the envelope. That is consistent during coexistence — gates
// reads the v1 run-state — and it is a cut-over blocker.
//
// This unit does NOT extend the envelope. §3.3 is normative; this is a finding
// against §3.3, not a licence to add the field. The seal therefore asserts the
// gap EXISTS, and turns red the day someone adds changed_files to v2 — at which
// point the finding has been addressed and both this row and the envelope key
// list must be updated together.
func TestSeal_Finding_V2EnvelopeCannotFeedGates(t *testing.T) {
	defer red(t)

	b, err := json.Marshal(ClassificationV2{})
	if err != nil {
		t.Fatal(err)
	}
	if hasKey(t, b, "changed_files") {
		t.Fatal("ClassificationV2 now carries changed_files. §3.3 does not, so this is an envelope extension: update v2EnvelopeKeys, the v2 goldens, and the design, and record who ruled on it.")
	}

	rs, err := GenerateReadSet(frozenConsumers)
	if err != nil {
		t.Fatalf("GenerateReadSet errored: %v", err)
	}
	gatesNeeds := false
	for _, f := range rs.Fields {
		if strings.HasPrefix(f.JSONPath, "changed_files") {
			for _, c := range f.Consumers {
				if c == "gates" {
					gatesNeeds = true
				}
			}
		}
	}
	if !gatesNeeds {
		t.Fatal("cmd/gates no longer declares changed_files — the cut-over blocker has cleared and this finding can be closed")
	}
	t.Log("RECORDED: cmd/gates reads changed_files; the §3.3 v2 envelope has no changed_files. " +
		"Migrating cmd/gates to the v2 sidecar is not possible without extending the envelope. " +
		"Consistent during coexistence (gates reads the v1 run-state); a cut-over blocker.")
}
