package main

// B1 BASELINE-SPLIT SEALS — cmd/classify/classify does three incompatible jobs.
//
// These rows are RED on purpose. Nothing here edits production code and nothing
// here rebuilds cmd/classify/classify.
//
// ─── THE CONFLATION ──────────────────────────────────────────────────────────
//
// One tracked file, `cmd/classify/classify`, is simultaneously:
//
//  1. THE DEPLOYED ARTIFACT. roles/tasker.md:224, skills/pr-raise.md:36 and
//     README.md:35 all exec ~/Project/claude-workflow/cmd/classify/classify by
//     absolute path. Nothing builds it at invocation, so those bytes are what
//     classifies real money diffs.
//  2. THE PINNED v1 DIFFERENTIAL BASELINE. pinnedV1 (seal_helpers_test.go) execs
//     it, and TestSeal_V1Differential_AgainstThePinnedBinary and
//     TestSeal_V1BytesAreIdenticalToThePinnedBinary compare live EmitV1 output
//     against what it prints.
//  3. THE DEFAULT `go build` OUTPUT PATH of this module.
//
// Every consequence is that one conflation:
//
//   - fixing main.go does not fix production, and cannot until the binary is
//     rebuilt and committed;
//   - rebuilding it silently retargets the differential at a build of the very
//     source the differential is supposed to be checking (the B1 panel rated
//     this HIGH: "one rebuild makes both seals tautologies");
//   - the env-var row on the deployed artifact (then
//     TestSeal_Recorded_EnvVarOutranksBothConfigDirectories, now
//     TestSeal_Shipped_EnvVarDoesNotOutrankTheConfigDirectories) was structurally
//     unfireable inside this unit — it measured the frozen artifact, so a source
//     fix left it green, which the P4 verified by construction;
//   - two authors have destroyed the file with their first command.
//
// ─── WHAT IS BEING SEALED (operator decision, already made) ──────────────────
//
// The differential gets its OWN baseline artifact: a content-addressed copy
// under cmd/classify/testdata/, its sha256 pinned as a constant in source and
// VERIFIED AT USE. cmd/classify/classify then becomes an ordinary build output
// that may be rebuilt and committed freely.
//
// The shape is D4's vendored-parser pin (role_protocol.TS_VENDORED_PARSER_*):
// a digest constant checked from disk on the path that uses the bytes, ordered
// first so nothing runs ahead of it. Two lessons carried over from D4, both
// sealed below:
//
//   - a digest RECOMPUTED FROM THE FILE pins nothing (row 2, leg B);
//   - a `stat`-based check cannot see a same-size one-byte flip, so the hash
//     must carry its own weight (row 2, leg B builds exactly that mutant).
//
// ─── THE HANDLE THESE ROWS USE, AND THE CONTRACT ON IT ───────────────────────
//
// `pinnedBinary` (seal_helpers_test.go:162) IS the differential's reference:
// pinnedV1 execs it and nothing else decides what the differential compares
// against. Every row below treats `pinnedBinary` as "whatever file the
// differential uses as its reference" and asserts properties of THAT file, so
// the body chooses the new path and these rows follow it.
//
// THE BODY MUST KEEP THE IDENTIFIER `pinnedBinary` NAMING THE FROZEN BASELINE.
// Renaming it is fine only if this file is updated in the same commit; pointing
// it at anything that is not the frozen v1 producer is caught by row 3 leg A.
//
// `deployedClassifyPath` below is the other half of the split: the artifact
// production execs. Today the two are the same file, and that identity is the
// defect (row 3 leg B).
//
// ─── NON-VACUITY ─────────────────────────────────────────────────────────────
//
// This unit has already produced FIVE distinct vacuous shapes: green on an
// unproducible input; a pass condition satisfiable by executing nothing; green
// on an incidental substring; a collapsed input space; and a recording that
// measures a frozen artifact so its trigger can never fire. Every row below
// therefore carries, in the same call:
//
//   - a CONTROL that must come out the other way, so the row can tell an
//     implementation from a constant;
//   - a FIXTURE-VALIDITY check that RAISES (never quietly passes) when the
//     fixture stops exhibiting what it is supposed to exhibit — a mutant that
//     turned out not to be inert, a source patch that changed no answer, a
//     clone whose bytes did not survive the copy;
//   - PROOF OF EXECUTION where the pass condition could be met by doing nothing
//     (readset_seal_test.go:1077 is the pattern). Here it takes two forms: a
//     child `go test` must be shown to have RUN the rows it is being judged on
//     by name, and a build must be shown to have WRITTEN the file it claims to
//     produce.
//
// Every red below was reproduced by hand before it was written down; the exact
// command and the exact observed outcome are recorded on each row.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// ─── the two artifacts the split separates ───────────────────────────────────

// deployedClassifyPath is the artifact PRODUCTION INVOKES.
//
// roles/tasker.md:224, skills/pr-raise.md:36 and README.md:35 all exec
// ~/Project/claude-workflow/cmd/classify/classify by absolute path, and nothing
// builds it at invocation time. It is also this module's default `go build`
// output path (README:18: "Build with `go build -o <name> .` in its
// directory"), which is what makes it unusable as a frozen reference.
const deployedClassifyPath = "./classify"

// baselineDigestOfRecord is the sha256 of the frozen v1 producer — the bytes
// tracked at git blob 9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0, which is what
// cmd/classify/classify contains at fix/B1-repair-adj@e39589a and what every
// differential row in this package has ever been measured against.
//
// MEASURED, not asserted:
//
//	$ git rev-parse HEAD:cmd/classify/classify
//	9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0
//	$ sha256sum cmd/classify/classify
//	ad289891e9c73306c8144cf8e50fd1e6662a02229f4c0d008fd87d1330cba28f
//
// A READER REPRODUCES THE BASELINE ARTIFACT with a byte copy of that blob:
//
//	git show 9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0 > <baseline path>
//	chmod +x <baseline path>
//
// No new artifact is committed by THIS commit. These rows need to know WHICH
// bytes are the reference, not to carry a second copy of them; the body creates
// the copy under testdata/ when it performs the split, and row 3 leg A is what
// stops the copy being re-derived from a rebuilt binary instead.
//
// WRITTEN IN TWO HALVES ON PURPOSE. Row 2 leg A scans this package's .go files
// for the digest as a contiguous literal, and this file must not be what
// satisfies that scan.
const baselineDigestOfRecord = "ad289891e9c73306c8144cf8e50fd1e6" +
	"662a02229f4c0d008fd87d1330cba28f"

// differentialRows are the two rows that ARE the v1 differential. Several rows
// below judge the differential by running exactly these and reading their
// verdicts, because a claim about "the differential's answer" that does not run
// the differential is a claim about a comment.
var differentialRows = []string{
	"TestSeal_V1Differential_AgainstThePinnedBinary",
	"TestSeal_V1BytesAreIdenticalToThePinnedBinary",
}

// shippedEnvVarRow is the row that judges the DEPLOYED artifact on whether
// $RISK_PATHS_CONFIG can redirect the money table.
//
// AMENDED BY THE P4 (B1-rebuild). It was `recordedEnvVarRow`, naming
// TestSeal_Recorded_EnvVarOutranksBothConfigDirectories — a recording of a live
// defect. The rebuild in this commit closed that defect in the artifact, the row
// fired on its designed trigger, and it has been inverted rather than deleted
// (contract_seal_test.go). A row whose name asserted the opposite of its body
// would be exactly the stale artifact this unit exists to eliminate, so the name
// moved with the polarity.
const shippedEnvVarRow = "TestSeal_Shipped_EnvVarDoesNotOutrankTheConfigDirectories"

// ─── ROW 1 ───────────────────────────────────────────────────────────────────

// ROW 1 — A `go build` MUST NOT BE ABLE TO MOVE THE DIFFERENTIAL'S REFERENCE.
//
// This is the row that has to fail today, and it is sealed so the trap cannot
// be re-sprung: it does not check a convention or a comment, it RUNS each
// documented build command in a throwaway clone and then looks at the reference
// file's bytes.
//
// The documented commands, all three of them:
//
//   - `go build -o classify .`  README:18, "Build with `go build -o <name> .`
//     in its directory" — the one a human follows
//   - `go build .`              the same thing with the default output name
//   - `go build ./...`          the command the B1 panel named
//
// MEASURED TODAY (clone of cmd/, run in cmd/classify):
//
//	$ sha256sum classify   ad289891...
//	$ go build ./...
//	$ sha256sum classify   962edf55...   (differs)
//
// So the reference moved, and the differential now compares live EmitV1 against
// a build of the same source it is checking.
//
// CONTROL / PROOF OF EXECUTION, judged in the same call: the deployed artifact
// MUST be written by each command — its mtime must advance past a timestamp
// this test set, and afterwards it must answer `classify capabilities`, which
// only a build of the CURRENT source can do (the frozen producer treats
// "capabilities" as a diff-file argument, capability.go:116). A build that
// silently did nothing fails the control and the row cannot pass by accident.
//
// WHAT CLOSES IT: the reference lives somewhere no build writes — a
// content-addressed copy under testdata/. Nothing about the deployed artifact
// changes; it stays exactly where README:18 says it is built.
func TestSeal_Baseline_DocumentedBuildCannotMoveTheDifferentialReference(t *testing.T) {
	defer red(t)

	// STRUCTURAL LEG. The reference must not BE the file the documented build
	// writes. os.SameFile rather than a path comparison, so a hard link or a
	// symlink between the two is caught as the same conflation.
	refInfo, err := os.Stat(pinnedBinary)
	if err != nil {
		t.Fatalf("the differential's reference %s is missing: %v", pinnedBinary, err)
	}
	depInfo, err := os.Stat(deployedClassifyPath)
	if err != nil {
		t.Fatalf("the deployed artifact %s is missing: %v — production execs it by absolute path", deployedClassifyPath, err)
	}
	if os.SameFile(refInfo, depInfo) {
		t.Errorf("THE DIFFERENTIAL'S REFERENCE AND THE DEPLOYED ARTIFACT ARE THE SAME FILE (%s == %s).\n"+
			"That file is also this module's default `go build` output path (README:18), so the reference is whatever was built last. Give the differential its own artifact under testdata/.",
			pinnedBinary, deployedClassifyPath)
	}

	// EMPIRICAL LEG. Run each documented command for real.
	for _, build := range [][]string{
		{"build", "-o", "classify", "."},
		{"build", "."},
		{"build", "./..."},
	} {
		label := "go " + strings.Join(build, " ")
		t.Run(label, func(t *testing.T) {
			defer red(t)
			pkg := cloneModule(t)

			refBefore := fileDigest(t, filepath.Join(pkg, pinnedBinary))

			// PROOF OF EXECUTION, part 1: backdate the deployed artifact so a
			// build that writes it is observable without deleting anything.
			stale := time.Unix(0, 0)
			if err := os.Chtimes(filepath.Join(pkg, deployedClassifyPath), stale, stale); err != nil {
				t.Fatalf("backdating the deployed artifact: %v", err)
			}

			if out, err := goTool(t, pkg, build...); err != nil {
				t.Fatalf("%s failed: %v\n%s", label, err, out)
			}

			info, err := os.Stat(filepath.Join(pkg, deployedClassifyPath))
			if err != nil {
				t.Fatalf("PROOF OF EXECUTION: %s did not leave a deployed artifact at %s: %v", label, deployedClassifyPath, err)
			}
			if !info.ModTime().After(stale) {
				t.Fatalf("PROOF OF EXECUTION: %s did not write %s (mtime still %v) — this row proves nothing about a command that did not run", label, deployedClassifyPath, info.ModTime())
			}
			// PROOF OF EXECUTION, part 2: what it wrote is a build of the
			// CURRENT source, which is the only reason the deployed path is
			// contested in the first place.
			probe := runLive(t, filepath.Join(pkg, deployedClassifyPath), pkg, nil, probeSubcommand)
			if probe.exit != 0 && probe.exit != exitCapabilityIncomplete {
				t.Fatalf("PROOF OF EXECUTION: after %s the deployed artifact does not answer %q (exit %d) — it is not a build of the current source\n%s", label, probeSubcommand, probe.exit, probe.all())
			}

			// THE ASSERTION.
			refAfter := fileDigest(t, filepath.Join(pkg, pinnedBinary))
			if refAfter != refBefore {
				t.Errorf("`%s` MOVED THE DIFFERENTIAL'S REFERENCE.\n"+
					"  %s: sha256 %s -> %s\n"+
					"The documented build command rewrote the file pinnedV1 execs, so the differential now compares live EmitV1 against a build of the very source it is checking. One rebuild makes both differential rows tautologies — the B1 panel's HIGH.\n"+
					"The reference must live where no build writes it.",
					label, pinnedBinary, refBefore[:12], refAfter[:12])
			}
		})
	}
}

// ─── ROW 2 ───────────────────────────────────────────────────────────────────

// ROW 2 — THE REFERENCE'S BYTES ARE PINNED AND CHECKED AT USE, NOT MERELY
// PRESENT.
//
// pinnedV1 (seal_helpers_test.go:167) checks that the reference EXISTS and
// nothing else. Presence is not identity: any executable at that path is
// accepted as the frozen v1 producer.
//
// LEG A — the digest is a constant IN SOURCE. D4's first lesson: a digest
// computed from the file it is supposed to pin pins nothing. So the value must
// be written down in tracked Go source, where a diff shows it changing. The
// scan looks for the digest of record as one contiguous 64-character literal in
// a .go file of this package; this file writes it in two halves precisely so it
// cannot be what satisfies the scan.
//
//	CONTROL, same call: the identical scan must find the literal `pinnedBinary`
//	— which is certainly present — so a scan that reads nothing, or reads the
//	wrong files, fails instead of reporting a satisfying absence.
//
// LEG B — the digest is checked AT USE, and it is a HASH.
//
// The mutant is built to defeat the two cheap non-answers at once:
//
//   - SAME SIZE, so a `stat`-based check cannot see it (D4's second lesson);
//   - BEHAVIOURALLY IDENTICAL, verified by running it, so a check that compares
//     output instead of bytes cannot see it either. The flipped byte is in the
//     middle of the longest run of NUL padding in the ELF image, and inertness
//     is PROVEN by execution, not assumed from the offset.
//
// That leaves exactly one way to notice: hashing the file and comparing to a
// pinned value. In particular THE "DIGEST RECOMPUTED FROM THE FILE" MUTATION
// dies here — a body that hashes the reference at use and compares the result
// to itself matches by construction and accepts this substitute.
//
// MEASURED TODAY (clone of cmd/, one byte at offset 2601969 flipped 0x00->0x01,
// size unchanged at 3916741, sha256 ad289891... -> eb6b9f94...):
//
//	$ go test -run '^(TestSeal_V1Differential_AgainstThePinnedBinary|TestSeal_V1BytesAreIdenticalToThePinnedBinary)$' .
//	ok
//
// The differential accepted a reference it had never seen before.
//
// CONTROL, same call: the same two rows on an untouched clone must PASS. A body
// that closes this row by making the differential unconditionally red fails
// here.
func TestSeal_Baseline_ReferenceBytesArePinnedInSourceAndCheckedAtUse(t *testing.T) {
	defer red(t)

	// ── LEG A: pinned in source ──
	scanned, hits := scanPackageSource(t, baselineDigestOfRecord)
	_, controlHits := scanPackageSource(t, "pinnedBinary")
	if len(controlHits) == 0 {
		t.Fatalf("CONTROL: scanned %d .go files and did not find the literal `pinnedBinary`, which is certainly there — the scan is not reading source, so its answer about the digest means nothing", scanned)
	}
	if len(hits) == 0 {
		t.Errorf("THE BASELINE'S DIGEST IS NOT PINNED IN SOURCE.\n"+
			"  looked for: %s (sha256 of the frozen v1 producer, git blob 9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0)\n"+
			"  scanned:    %d .go files in this package (this file excluded by construction — it writes the digest in two halves)\n"+
			"A digest recomputed from the artifact at use pins nothing: it agrees with whatever bytes are on disk. The value has to be written down where a diff shows it changing — that is the whole of D4's TS_VENDORED_PARSER_SHA256 lesson.",
			baselineDigestOfRecord, scanned)
	}

	// ── LEG B: checked at use, and it is a hash ──
	control := cloneModule(t)
	controlVerdicts, controlOut := runRows(t, control, differentialRows)
	for _, row := range differentialRows {
		if controlVerdicts[row] != "PASS" {
			t.Fatalf("CONTROL: %s is %s on an UNTOUCHED clone. The differential must agree with the genuine reference before its reaction to a substituted one means anything.\n%s", row, controlVerdicts[row], controlOut)
		}
	}

	pkg := cloneModule(t)
	refPath := filepath.Join(pkg, pinnedBinary)
	mutant, offset := sameSizeInertMutant(t, refPath)
	original := fileDigest(t, refPath)
	if err := os.WriteFile(refPath, mutant, 0o700); err != nil { // #nosec G306 -- an executable in this test's own scratch clone
		t.Fatal(err)
	}
	substituted := fileDigest(t, refPath)

	// FIXTURE VALIDITY — raise rather than pass quietly if the substitution
	// stopped being the thing this row is about.
	if substituted == original {
		t.Fatalf("the mutant hashes the same as the original — nothing was substituted and this row proves nothing")
	}

	got, out := runRows(t, pkg, differentialRows)
	var accepted []string
	for _, row := range differentialRows {
		if got[row] == "PASS" {
			accepted = append(accepted, row)
		}
	}
	if len(accepted) > 0 {
		t.Errorf("THE DIFFERENTIAL ACCEPTED A REFERENCE IT HAD NEVER SEEN.\n"+
			"  reference: %s\n"+
			"  sha256:    %s -> %s\n"+
			"  size:      %d bytes, UNCHANGED — one byte flipped 0x00 -> 0x01 at offset %d, in the middle of the image's longest run of NUL padding\n"+
			"  behaviour: IDENTICAL, verified by running both over a fixture before the substitution\n"+
			"  and still PASSED: %v\n"+
			"pinnedV1 (seal_helpers_test.go:167) checks only that the file EXISTS. Presence is not identity.\n"+
			"A `stat` cannot see this substitution and neither can an output comparison, which is the point: the only check that catches it is a sha256 compared against a value pinned in source. A digest recomputed from the file at use would match this mutant by construction.",
			pinnedBinary, original[:12], substituted[:12], len(mutant), offset, accepted)
		t.Logf("child output:\n%s", out)
	}
}

// ─── ROW 3 ───────────────────────────────────────────────────────────────────

// ROW 3 — THE DIFFERENTIAL IS NOT A TAUTOLOGY: THE SUBJECT AND THE REFERENCE
// CANNOT BE THE SAME ARTIFACT.
//
// The subject is live EmitV1, compiled from the tree under review. The
// reference is supposed to be a producer that PREDATES the tree. Today the two
// are kept apart by convention alone — the reference is the default build
// output path, so any build collapses them, and nothing anywhere states which
// bytes the reference is supposed to be.
//
// LEG A — THE CONTENT ADDRESS. The reference must hash to the frozen v1
// producer's digest of record. GREEN TODAY and it must stay green: this is the
// row that stops the split being "performed" by copying a freshly rebuilt
// binary into testdata/, which would move the baseline forward while looking
// exactly like the fix. Its trigger is any re-derivation of the baseline.
//
// LEG B — THE STRUCTURE. The reference must not be the same file as the
// deployed artifact. RED TODAY: they are one file.
//
// LEG C — THE COLLAPSE, DEMONSTRATED. Build the tree over the reference and the
// differential must refuse. MEASURED TODAY (clone of cmd/):
//
//	$ go build -o /tmp/fresh . && cp /tmp/fresh ./classify
//	$ go test -run '^(TestSeal_V1Differential…|TestSeal_V1Bytes…)$' .
//	ok
//
// The differential compared the source against itself and reported agreement.
//
// CONTROL, same call: the same two rows on an untouched clone PASS, so leg C is
// reading a reaction to the substitution and not a suite that is simply red.
//
// WHY THIS IS NOT ROW 2 AGAIN. Row 2 substitutes bytes that behave identically;
// it forces a real hash. Row 3 substitutes THE SUBJECT ITSELF; it forces the
// reference to be something the tree cannot produce. A body that pins the
// digest closes both, and that is fine — they are different mutations of
// different defects, and a body that closes one by accident has still had to
// close it.
func TestSeal_Baseline_ReferenceIsNeitherTheSubjectNorTheDeployedArtifact(t *testing.T) {
	defer red(t)

	// ── LEG A: the content address ──
	if got := fileDigest(t, pinnedBinary); got != baselineDigestOfRecord {
		t.Errorf("THE DIFFERENTIAL'S REFERENCE IS NOT THE FROZEN v1 PRODUCER.\n"+
			"  %s hashes to %s\n"+
			"  the frozen producer is  %s  (git blob 9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0)\n"+
			"If the baseline was re-derived from a rebuilt binary, the differential has quietly started comparing the tree against a recent build of itself — which is the tautology this unit exists to prevent, arriving through the fix rather than through a stray `go build`.\n"+
			"If the baseline was deliberately moved forward, that is an operator decision: update baselineDigestOfRecord in the same commit and say what the new reference is and why it predates the tree.",
			pinnedBinary, got[:12], baselineDigestOfRecord[:12])
	}

	// ── LEG B: the structure ──
	refInfo, err := os.Stat(pinnedBinary)
	if err != nil {
		t.Fatalf("the differential's reference %s is missing: %v", pinnedBinary, err)
	}
	depInfo, err := os.Stat(deployedClassifyPath)
	if err != nil {
		t.Fatalf("the deployed artifact %s is missing: %v", deployedClassifyPath, err)
	}
	if os.SameFile(refInfo, depInfo) {
		t.Errorf("THE REFERENCE IS THE DEPLOYED ARTIFACT (%s and %s are one file).\n"+
			"The deployed artifact is what production execs and what every build writes; the reference is supposed to predate the tree. While they are the same file the differential's independence is a convention, and conventions are what `go build` ignores.",
			pinnedBinary, deployedClassifyPath)
	}

	// ── LEG C: the collapse, demonstrated ──
	control := cloneModule(t)
	controlVerdicts, controlOut := runRows(t, control, differentialRows)
	for _, row := range differentialRows {
		if controlVerdicts[row] != "PASS" {
			t.Fatalf("CONTROL: %s is %s on an UNTOUCHED clone; leg C below cannot distinguish a refusal from a suite that is already red.\n%s", row, controlVerdicts[row], controlOut)
		}
	}

	pkg := cloneModule(t)
	refPath := filepath.Join(pkg, pinnedBinary)
	before := fileDigest(t, refPath)
	subject := filepath.Join(t.TempDir(), "subject")
	if out, err := goTool(t, pkg, "build", "-o", subject, "."); err != nil {
		t.Fatalf("building the subject failed: %v\n%s", err, out)
	}
	subjectBytes, err := os.ReadFile(subject) // #nosec G304 -- this test's own scratch build
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refPath, subjectBytes, 0o700); err != nil { // #nosec G306 -- an executable in this test's own scratch clone
		t.Fatal(err)
	}
	// FIXTURE VALIDITY: if a build of the tree happened to be byte-identical to
	// the frozen producer there would be nothing to demonstrate, and the row
	// must say so rather than pass.
	if fileDigest(t, refPath) == before {
		t.Fatalf("a build of the current tree is byte-identical to the frozen v1 producer — there is no substitution here and leg C proves nothing")
	}

	got, out := runRows(t, pkg, differentialRows)
	var agreed []string
	for _, row := range differentialRows {
		if got[row] == "PASS" {
			agreed = append(agreed, row)
		}
	}
	if len(agreed) > 0 {
		t.Errorf("THE DIFFERENTIAL COMPARED THE SOURCE AGAINST ITSELF AND REPORTED AGREEMENT.\n"+
			"  the reference %s was replaced by a build of the tree under review (sha256 %s -> %s)\n"+
			"  and these still PASSED: %v\n"+
			"A differential whose reference can be the subject measures nothing. The reference must be an artifact this tree cannot produce — content-addressed, pinned, and checked before it is used.",
			pinnedBinary, before[:12], fileDigest(t, refPath)[:12], agreed)
		t.Logf("child output:\n%s", out)
	}
}

// ─── ROW 4 ───────────────────────────────────────────────────────────────────

// ROW 4 — REBUILDING AND COMMITTING THE DEPLOYED BINARY CHANGES NO DIFFERENTIAL
// ANSWER.
//
// This is the whole point of the split, and it is directly measurable: run the
// differential, rebuild the deployed artifact with the documented command, run
// the differential again, compare the VERDICTS.
//
// A row that only did that on an unmodified tree would be vacuous — the
// verdicts are PASS either side today, so nothing would be proven. So the
// measurement is taken on a tree whose v1 output DIVERGES from the reference,
// where the rebuild has an answer to change:
//
//	main.go:127  `json:"risk_reasons,omitempty"` -> `json:"risk_reasonz,omitempty"`
//
// a one-line, single-occurrence rename of a v1 wire key, applied to a clone.
// The patch is asserted to be unique before it is applied, and the divergence
// it is supposed to cause is asserted to have actually happened (FIXTURE
// VALIDITY) before the rebuild is allowed to mean anything.
//
// MEASURED TODAY (clone of cmd/, patch applied):
//
//	$ go test -run '^(…Differential…|…V1Bytes…)$' .   FAIL
//	$ go build ./...
//	$ go test -run '^(…Differential…|…V1Bytes…)$' .   ok
//
// The rebuild ERASED a real divergence. Nothing about the wire got fixed
// between those two commands; the reference simply became the broken build.
//
// CONTROL, same call: the identical before/after on an UNPATCHED clone must
// give the same verdicts both times (PASS, PASS). That control fails if a body
// closes this row by making the differential unconditionally red, and it also
// fails if the rebuild breaks the suite for some unrelated reason.
//
// PROOF OF EXECUTION: runRows fails outright unless the child `go test` reports
// both rows by name, so a build error or an empty `-run` match cannot be read
// as "the verdicts did not change".
func TestSeal_Baseline_RebuildingTheDeployedArtifactChangesNoDifferentialAnswer(t *testing.T) {
	defer red(t)

	// ── CONTROL: unpatched tree, verdicts must survive the rebuild ──
	control := cloneModule(t)
	c0, cOut0 := runRows(t, control, differentialRows)
	if out, err := goTool(t, control, "build", "./..."); err != nil {
		t.Fatalf("CONTROL: go build ./... failed: %v\n%s", err, out)
	}
	c1, cOut1 := runRows(t, control, differentialRows)
	for _, row := range differentialRows {
		if c0[row] != "PASS" {
			t.Fatalf("CONTROL: %s is %s before any rebuild on an untouched clone.\n%s", row, c0[row], cOut0)
		}
		if c1[row] != c0[row] {
			t.Fatalf("CONTROL: %s went %s -> %s across a rebuild of an UNPATCHED tree. Rebuilding must not disturb an agreeing differential either.\n%s", row, c0[row], c1[row], cOut1)
		}
	}

	// ── THE DEFECT: a tree that diverges, and a rebuild that hides it ──
	pkg := cloneModule(t)
	patchSource(t, filepath.Join(pkg, "main.go"),
		`json:"risk_reasons,omitempty"`, `json:"risk_reasonz,omitempty"`)

	before, beforeOut := runRows(t, pkg, differentialRows)
	diverged := false
	for _, row := range differentialRows {
		if before[row] == "FAIL" {
			diverged = true
		}
	}
	// FIXTURE VALIDITY — raise, do not pass quietly.
	if !diverged {
		t.Fatalf("renaming the v1 wire key risk_reasons did NOT make the differential diverge (%v).\n"+
			"Either the differential no longer compares v1 keys or the fixtures no longer carry that one. Re-derive the mutation before trusting this row.\n%s", before, beforeOut)
	}

	refBefore := fileDigest(t, filepath.Join(pkg, pinnedBinary))
	if out, err := goTool(t, pkg, "build", "./..."); err != nil {
		t.Fatalf("go build ./... failed: %v\n%s", err, out)
	}
	after, afterOut := runRows(t, pkg, differentialRows)
	refAfter := fileDigest(t, filepath.Join(pkg, pinnedBinary))

	var flipped []string
	for _, row := range differentialRows {
		if after[row] != before[row] {
			flipped = append(flipped, fmt.Sprintf("%s: %s -> %s", row, before[row], after[row]))
		}
	}
	if len(flipped) > 0 {
		t.Errorf("REBUILDING THE DEPLOYED BINARY CHANGED THE DIFFERENTIAL'S ANSWER.\n"+
			"  the tree renames the v1 wire key risk_reasons -> risk_reasonz, which the reference does not have\n"+
			"  before `go build ./...`: %v\n"+
			"  after  `go build ./...`: %v\n"+
			"  and the reference's own bytes moved: %s -> %s\n"+
			"Nothing was fixed between those two commands. The rebuild made the reference a copy of the broken subject, so the divergence had nobody left to disagree with. Any wire break can be committed this way and the differential will certify it.\n"+
			"After the split the deployed artifact is an ordinary build output and this must read %v both times.",
			before, after, refBefore[:12], refAfter[:12], before)
		t.Logf("child output after the rebuild:\n%s", afterOut)
	}
}

// ─── ROW 5 ───────────────────────────────────────────────────────────────────

// ROW 5 — THE SHIPPED-ARTIFACT ROW TRACKS THE DEPLOYED ARTIFACT, AND ONLY IT.
//
// ─── THE DECISION, AND WHY ───────────────────────────────────────────────────
//
// TestSeal_Shipped_EnvVarDoesNotOutrankTheConfigDirectories KEEPS MEASURING THE
// DEPLOYED ARTIFACT. It is not repointed at source, it is not deleted, and it
// is not weakened. A NEW row is not needed for source observability either,
// because one already exists and governs:
// TestSeal_Repair_EnvVarMustNotOutrankTheWorktreeMoneyTable builds the tree to
// a scratch path and blocks the bypass in SOURCE (P4 ruling, dispute 1).
//
// So the two subjects are already covered, and the reason for keeping a row on
// the artifact survives the split intact: between a source fix and a rebuild,
// this is the ONLY row in the suite that can tell "fixed" from "shipped".
// Repointing it at source would delete exactly that distinction.
//
// THE TRAP THIS ROW EXISTS TO CATCH: a body that "splits" by repointing
// `pinnedBinary` at the frozen testdata copy and leaves the shipped-artifact row
// exec'ing `pinnedBinary`. That row would then measure a file that is frozen BY
// CONSTRUCTION and could never respond to anything again.
//
// ─── P4 AMENDMENT (adjudicate(B1-rebuild)) · POLARITY INVERTED ───────────────
//
// This row was written while the deployed artifact still carried the bypass, so
// it demonstrated sensitivity in the only direction available then: install a
// REPAIRED artifact, watch the recording go RED. This commit rebuilt and
// committed cmd/classify/classify, so the repaired artifact is now what an
// untouched clone contains, and that construction has no defect left to install.
//
// The property being sealed is unchanged — THE SHIPPED-ARTIFACT ROW'S VERDICT IS
// A FUNCTION OF THE BYTES AT `deployedClassifyPath` — so the demonstration is
// simply mirrored: install an UNREPAIRED artifact and watch the row go RED.
//
// AND THE UNREPAIRED ARTIFACT IS FREE. It is the frozen v1 producer already
// tracked under testdata/: content-addressed, sha256-pinned, verified at use,
// and permanently in possession of the bypass. The previous version patched
// main.go:403 and built a candidate; that machinery is deleted, because a patch
// that must be re-derived whenever main.go moves is a maintenance burden the
// pinned fixture does not have. `occurrences`, `patchSource` on this path and
// the scratch build all go with it.
//
// ─── HOW IT IS MEASURED ──────────────────────────────────────────────────────
//
// Install the frozen v1 producer at the deployed path in a clone, and require
// BOTH halves at once:
//
//	(a) the shipped-artifact row goes RED, for its own stated reason; and
//	(b) the differential's reference is byte-unchanged and the differential
//	    still agrees.
//
// (b) is the half that could not hold before the split: back then ./classify and
// the reference were one file, so installing anything at the deployed path
// re-derived the baseline (measured: ad289891... -> 52d92ecf...). It holds now
// because they are two files, and that is the whole point of the split.
//
// The unrepaired artifact is VERIFIED BY OBSERVATION rather than by assumption:
// v1 is run with the variable pointing at an attacker table and must report the
// attacker table. A fixture that is not actually bypassable cannot make anything
// below mean what it says.
//
// CONTROL, same call: on an untouched clone the shipped-artifact row must PASS.
// It now records a bypass that is CLOSED in the shipped artifact; if it is red
// before anything is installed, nothing below can be read as the installation
// having caused it.
func TestSeal_Baseline_ShippedEnvVarRowFiresOnAnUnrepairedDeployedArtifact(t *testing.T) {
	defer red(t)

	rows := []string{shippedEnvVarRow}

	// ── CONTROL: the row is green against the artifact as shipped ──
	control := cloneModule(t)
	cv, cOut := runRows(t, control, rows)
	if cv[shippedEnvVarRow] != "PASS" {
		t.Fatalf("CONTROL: %s is %s on an UNTOUCHED clone. It records that the bypass is CLOSED in the shipped artifact; if it is already red, nothing below can be read as the installed artifact having caused it.\n%s", shippedEnvVarRow, cv[shippedEnvVarRow], cOut)
	}

	// ── the unrepaired artifact: the frozen v1 producer, checked then observed ──
	if err := VerifyPinnedBaseline(pinnedBinary); err != nil {
		t.Fatalf("the unrepaired artifact this row installs is not the frozen v1 producer: %v", err)
	}
	unrepaired, err := os.ReadFile(pinnedBinary) // #nosec G304 -- the tracked baseline fixture
	if err != nil {
		t.Fatal(err)
	}

	// FIXTURE VALIDITY, BY OBSERVATION. Do not assume v1 is bypassable: run it.
	money, drifted := realTable(t), driftedTable(t)
	assertTablesDisagreeOnMoney(t, money, drifted)
	wt := writeDual(t, money, nil)
	attacker := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(attacker, drifted, 0o600); err != nil {
		t.Fatal(err)
	}
	diffPath := writeDiff(t, wt, walletPath)
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	check := runLive(t, filepath.Join(pkgDir, pinnedBinary), pkgDir, []string{"RISK_PATHS_CONFIG=" + attacker},
		"-json", "-no-git", "-worktree", wt, diffPath)
	if check.exit != 0 {
		t.Fatalf("the frozen v1 producer exited %d, not 0 — it is not a working classifier and cannot stand in for an unrepaired deployment\n%s", check.exit, check.all())
	}
	if got := check.json(t)["config_path"]; got != attacker {
		t.Fatalf("the frozen v1 producer did NOT let $RISK_PATHS_CONFIG govern (config_path = %v, want the attacker table %q). This row needs a genuinely bypassable artifact to install; if v1 is not bypassable, the finding this whole unit is built on has been mis-stated.", got, attacker)
	}

	// ── install it at the DEPLOYED path in a fresh clone ──
	pkg := cloneModule(t)
	refBefore := fileDigest(t, filepath.Join(pkg, pinnedBinary))
	if err := os.WriteFile(filepath.Join(pkg, deployedClassifyPath), unrepaired, 0o700); err != nil { // #nosec G306 -- an executable in this test's own scratch clone
		t.Fatal(err)
	}

	// (a) the row must fire, for its own reason.
	got, out := runRows(t, pkg, rows)
	if got[shippedEnvVarRow] != "FAIL" {
		t.Errorf("THE SHIPPED-ARTIFACT ROW DID NOT FIRE AGAINST AN UNREPAIRED DEPLOYED ARTIFACT (%s).\n"+
			"  %s was replaced by the frozen v1 producer, in which $RISK_PATHS_CONFIG DOES govern — verified by running it — and the row is still %s.\n"+
			"It is measuring something other than the deployed artifact. If it now judges only the frozen baseline under testdata/, its verdict can never respond to a rebuild again: its job is to say whether the bypass is live in the artifact production runs.\n%s",
			shippedEnvVarRow, deployedClassifyPath, got[shippedEnvVarRow], out)
	} else if !strings.Contains(out, "OUTRANKS THE CONFIG DIRECTORIES IN THE DEPLOYED ARTIFACT AGAIN") {
		// A red for the wrong reason is not this row firing. This is a check on
		// the REASON for a failure, never a pass condition.
		t.Errorf("%s failed, but not with its own trigger message (\"OUTRANKS THE CONFIG DIRECTORIES IN THE DEPLOYED ARTIFACT AGAIN\"). It went red for some other reason, so this row has not observed it firing.\n%s", shippedEnvVarRow, out)
	}

	// (b) and the differential must not have noticed any of it.
	refAfter := fileDigest(t, filepath.Join(pkg, pinnedBinary))
	if refAfter != refBefore {
		t.Errorf("MAKING THE SHIPPED-ARTIFACT ROW FIRE DISTURBED THE DIFFERENTIAL'S BASELINE.\n"+
			"  writing an unrepaired artifact to %s changed %s: sha256 %s -> %s\n"+
			"These are supposed to be two files after the split. If writing one moves the other, the split has been undone — by a hard link, a symlink, or a path that resolves back to the same inode — and the repo is back to being able to have a responsive artifact row or a meaningful differential, not both.",
			deployedClassifyPath, pinnedBinary, refBefore[:12], refAfter[:12])
	}
	dv, dOut := runRows(t, pkg, differentialRows)
	for _, row := range differentialRows {
		if dv[row] != "PASS" {
			t.Errorf("after an unrepaired artifact was installed at %s, %s is %s. What sits at the deployed path must not be able to disturb the differential, in either direction.\n%s", deployedClassifyPath, row, dv[row], dOut)
		}
	}
}

// ─── ROW 6 · THE RESIDUAL P4 NAMED ───────────────────────────────────────────
//
// THE SOURCE-VS-BINARY DRIFT GUARD.
//
// P4 (adjudicate(B1-repair), on TestSeal_Recorded_V1ProjectionDoesNotSurviveGates)
// named the residual and declined to close it:
//
//	"THE ONE WAY IT COULD ROT into the env-var row's shape: a cmd/gates source
//	fix merged WITHOUT rebuilding the tracked binary. This row would then be
//	green, accurate about the shipped artifact, and silently stale about the
//	source. P4 did not add a source-vs-binary drift guard — that is new
//	machinery — but it is the one guard this row wants."
//
// IT IS SEALABLE HERE, and it needs no production machinery: a committed binary
// is a build of committed source, and both are on disk. The guard builds the
// source to a scratch path and requires the committed artifact to ANSWER THE
// SAME WAY over a battery of invocations.
//
// BEHAVIOUR, NOT BYTES, and that is a deliberate choice. A byte-exact
// comparison would be the stronger claim but it is not portable: Go embeds a
// build ID derived from the toolchain and the build environment, so a
// byte-exact guard reports drift on any machine with a different Go release and
// would be red in CI for reasons that have nothing to do with anybody's source.
// A behavioural fingerprint is stable across toolchains and catches exactly the
// class the residual is about — a source fix that has not reached the shipped
// artifact.
//
// WHY THIS ROW IS NOT VACUOUS. The comparator is exercised in both directions
// in the same call:
//
//   - it must report NO drift between a fresh build and itself (so it is not
//     always-red);
//   - it must report DRIFT for a build of deliberately mutated source (so it is
//     not always-green — this is the proof the guard can fire, and it is what
//     the gates row below rests on entirely, since that one is green today).
//
// ─── ROW 6a: cmd/classify ────────────────────────────────────────────────────
//
// RED TODAY, and this is the first consequence named in the unit brief — fixing
// source does not fix production:
//
//	$ ./classify capabilities          => exit 3, INVALID_INPUT, "no risk-paths
//	                                      config found" (it read "capabilities"
//	                                      as a diff path)
//	$ go build -o /tmp/fresh . && /tmp/fresh capabilities
//	                                   => exit 0, {"probe_version":1,...}
//
// The committed deployed artifact predates the capability probe entirely. Every
// repair in this unit is in the same position: green in the tree, absent from
// the artifact that classifies real money diffs.
//
// WHAT CLOSES IT: `go build -o classify .` in cmd/classify, committed. That is
// safe only after the split — which is why this row belongs to this unit and
// not to the repair unit, and why it must not be closed by rebuilding before
// the baseline has its own artifact. Rows 1-4 are what make the rebuild safe;
// this row is what makes it happen.
func TestSeal_Drift_CommittedClassifyIsAFaithfulBuildOfItsSource(t *testing.T) {
	defer red(t)

	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := classifyProbeCases(t)

	// The reference build: current source, scratch path, baseline untouched.
	fresh := buildTo(t, pkgDir, "classify-fresh")
	freshPrint := fingerprint(t, fresh, pkgDir, cases)

	// CONTROL 1 — the comparator is not always-red. A second build of the same
	// source at a different path must fingerprint identically, which also
	// proves the normalisation does its job on argv[0] and on temp paths.
	twin := buildTo(t, pkgDir, "classify-twin")
	if twinPrint := fingerprint(t, twin, pkgDir, cases); twinPrint != freshPrint {
		t.Fatalf("CONTROL: two builds of the SAME source do not fingerprint alike, so \"drifted\" would mean nothing.\n%s", firstDifference(freshPrint, twinPrint))
	}

	// CONTROL 2 — the comparator is not always-green. A build of deliberately
	// mutated source must be reported as drifted. This is the proof that the
	// guard can fire.
	mutated := cloneModule(t)
	patchSource(t, filepath.Join(mutated, "main.go"),
		`json:"risk_reasons,omitempty"`, `json:"risk_reasonz,omitempty"`)
	mutantBin := buildTo(t, mutated, "classify-mutant")
	if mutantPrint := fingerprint(t, mutantBin, pkgDir, cases); mutantPrint == freshPrint {
		t.Fatalf("CONTROL: a build of MUTATED source fingerprints identically to the real one. The battery does not observe the source, so it cannot detect drift in it.")
	}

	// THE MEASUREMENT.
	committedPrint := fingerprint(t, filepath.Join(pkgDir, deployedClassifyPath), pkgDir, cases)
	if committedPrint != freshPrint {
		t.Errorf("THE COMMITTED cmd/classify/classify IS NOT A BUILD OF THE COMMITTED SOURCE.\n"+
			"%s\n"+
			"That binary is what production execs — roles/tasker.md:224, skills/pr-raise.md:36, README.md:35 all name it by absolute path, and nothing builds it at invocation. So every fix in the tree is green here and absent from the artifact that classifies real money diffs.\n"+
			"Close it with `go build -o classify .` in cmd/classify, committed. Do that only AFTER the differential has its own baseline under testdata/ — before the split, that same command destroys the reference (rows 1-4).",
			firstDifference(committedPrint, freshPrint))
	}
}

// ─── ROW 6b: cmd/gates — the residual, sealed ────────────────────────────────
//
// GREEN TODAY, and that is the point: it is a guard, not a finding.
//
// VERIFIED, not assumed, before it was written: the committed cmd/gates/gates
// and a fresh build of cmd/gates answer identically over the recording's own
// invocation and over its usage text (the only difference was argv[0] and the
// run-state path, both normalised). So P4's ruling that
// TestSeal_Recorded_V1ProjectionDoesNotSurviveGates "describes both" the binary
// and its source is true TODAY, and this row is what keeps it true.
//
// ITS TRIGGER: a cmd/gates source change merged without rebuilding the tracked
// binary. That is precisely the rot P4 named — the recording would stay green,
// accurate about the shipped artifact and silently stale about the source — and
// CI would not catch it either, because .github/workflows/gates.yml runs
// `go test` over the checked-out tree and never rebuilds.
//
// WHAT TO DO WHEN IT FIRES: rebuild and commit cmd/gates/gates in the same
// commit as the source change, which is already this repo's convention
// (bdecc7f). Then re-read TestSeal_Recorded_V1ProjectionDoesNotSurviveGates:
// if the source change fixed the classification loss, that recording turns red
// too, and that is the good news it exists to deliver.
//
// SCOPE NOTE, stated rather than assumed: this row lives in cmd/classify's
// suite because that is where the recording it protects lives. It reads
// ../gates the same way TestSeal_Recorded_V1ProjectionDoesNotSurviveGates
// already does and writes nothing there — asserted below, not hoped for.
func TestSeal_Drift_CommittedGatesIsAFaithfulBuildOfItsSource(t *testing.T) {
	defer red(t)

	gatesDir := filepath.Join("..", "gates")
	gatesBin := filepath.Join(gatesDir, "gates")
	for _, p := range []string{gatesBin, filepath.Join(gatesDir, "main.go"), filepath.Join(gatesDir, "testdata", "example-gates.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s is missing: %v — it is a tracked fixture and this guard cannot run without it", p, err)
		}
	}
	committedBefore := fileDigest(t, gatesBin)

	cases := gatesProbeCases(t)

	fresh := buildTo(t, gatesDir, "gates-fresh")
	freshPrint := fingerprint(t, fresh, gatesDir, cases)

	// CONTROL 1 — not always-red.
	twin := buildTo(t, gatesDir, "gates-twin")
	if twinPrint := fingerprint(t, twin, gatesDir, cases); twinPrint != freshPrint {
		t.Fatalf("CONTROL: two builds of the SAME cmd/gates source do not fingerprint alike, so \"drifted\" would mean nothing.\n%s", firstDifference(freshPrint, twinPrint))
	}

	// CONTROL 2 — not always-green. THIS IS THE ROW'S NON-VACUITY: the row is
	// green today, so the only thing that makes it evidence is a demonstration
	// that it goes red when the source moves and the binary does not. That is
	// exactly what this leg builds.
	mutated := cloneSibling(t, gatesDir)
	patchSource(t, filepath.Join(mutated, "main.go"),
		`log.Printf("gate results written to %s", opts.runState)`,
		`log.Printf("gate results were written to %s", opts.runState)`)
	mutantBin := buildTo(t, mutated, "gates-mutant")
	if mutantPrint := fingerprint(t, mutantBin, gatesDir, cases); mutantPrint == freshPrint {
		t.Fatalf("CONTROL: a build of MUTATED cmd/gates source fingerprints identically to the real one, so this guard could not notice a source fix that never reached the binary — which is the entire residual it is here to close.")
	}

	// THE MEASUREMENT.
	committedPrint := fingerprint(t, gatesBin, gatesDir, cases)
	if committedPrint != freshPrint {
		t.Errorf("THE COMMITTED cmd/gates/gates IS NOT A BUILD OF THE COMMITTED cmd/gates SOURCE.\n"+
			"%s\n"+
			"TestSeal_Recorded_V1ProjectionDoesNotSurviveGates execs that binary, so while they disagree the recording is a statement about the shipped artifact only and is silently stale about the source. .github/workflows/gates.yml runs `go test` over the checked-out tree and never rebuilds, so CI will not tell anyone either.\n"+
			"Rebuild and commit cmd/gates/gates alongside the source change (this repo's convention — bdecc7f), then re-read that recording: if the classification loss is fixed, it turns red, and that is the good news it exists to deliver.",
			firstDifference(committedPrint, freshPrint))
	}

	// This guard must not become the thing it guards against.
	if after := fileDigest(t, gatesBin); after != committedBefore {
		t.Errorf("THIS ROW REWROTE %s (%s -> %s). Every build here goes to a scratch path; restore it with `git checkout -- cmd/gates/gates`.", gatesBin, committedBefore[:12], after[:12])
	}
}

// ═══ machinery ═══════════════════════════════════════════════════════════════

// cloneModule copies the whole cmd/ tree — this module and its siblings — into
// a throwaway directory and returns the clone's cmd/classify.
//
// The siblings come along because the differential needs them:
// TestSeal_V1Differential_AgainstThePinnedBinary calls GenerateReadSet, which
// reads ../gates and ../iterate and FAILS rather than degrades when it cannot
// (readset_seal_test.go:777). A clone of cmd/classify alone would make every
// row that runs the differential red for the wrong reason.
//
// THE CLONE CARRIES NO .git MARKER, and that is a considered choice with one
// consequence worth stating rather than discovering. readset.go's repoRelative
// (readset.go:479) walks up for a .git entry to render citations as
// "cmd/gates/main.go"; with no root marker it returns "../gates/main.go"
// instead. Only ONE row reads those strings —
// TestSeal_GenerateReadSet_CitationsResolveToRealLines — and no row here ever
// runs it in a clone. The rows that ARE run in clones (the two differential
// rows and the env-var recording) assert on JSON paths and process output, not
// on citation text, and both were confirmed by hand to pass in a clone and to
// fail there when the reference is substituted.
//
// The alternative was tried and rejected: planting an empty .git directory
// turns on the go tool's VCS stamping, which then fails ("error obtaining VCS
// status: exit status 128") and makes every documented build in row 1 fail for
// a reason that has nothing to do with anybody's baseline. A red for the wrong
// reason is worse than the narrow infidelity above.
//
// It verifies the copy by digest, because a row that mutates "the reference" in
// a clone is measuring nothing if the clone's reference was not the reference.
func cloneModule(t *testing.T) string {
	t.Helper()
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "cmd")
	copyTree(t, filepath.Dir(pkgDir), dst)
	pkg := filepath.Join(dst, filepath.Base(pkgDir))

	if got, want := fileDigest(t, filepath.Join(pkg, pinnedBinary)), fileDigest(t, filepath.Join(pkgDir, pinnedBinary)); got != want {
		t.Fatalf("the clone's %s does not match the original (%s vs %s) — the copy did not preserve the bytes these rows are about", pinnedBinary, got[:12], want[:12])
	}
	return pkg
}

// cloneSibling copies one sibling module directory and returns the copy.
func cloneSibling(t *testing.T, dir string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(dir))
	copyTree(t, dir, dst)
	return dst
}

// copyTree copies a directory tree, preserving file modes. There are no
// symlinks under cmd/ and it refuses rather than guesses if one appears.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, 0o750)
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink; this copier does not guess at link semantics", path)
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular file (%v)", path, info.Mode())
		}
		in, err := os.Open(path) // #nosec G304 -- a path under the repo's own cmd/ tree
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()) // #nosec G304 -- a path under this test's own temp dir
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
	if err != nil {
		t.Fatalf("cloning %s: %v", src, err)
	}
}

// goTool runs the go command in dir and returns its combined output.
func goTool(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", args...) // #nosec G204 -- a fixed set of build/test invocations from this file
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// buildTo builds the module in dir to a scratch path and returns it.
//
// It carries the same guard liveClassify does, for the same reason: `go build`
// with no -o writes the module's default output path, and in cmd/classify that
// path is the file this whole unit is about. Two authors have sprung that trap.
func buildTo(t *testing.T, dir, name string) string {
	t.Helper()
	defaultOut := filepath.Join(dir, filepath.Base(dir))
	before, haveDefault := "", false
	if _, err := os.Stat(defaultOut); err == nil {
		before, haveDefault = fileDigest(t, defaultOut), true
	}

	out := filepath.Join(t.TempDir(), name)
	if msg, err := goTool(t, dir, "build", "-o", out, "."); err != nil {
		t.Fatalf("building %s failed: %v\n%s", dir, err, msg)
	}

	if haveDefault {
		if after := fileDigest(t, defaultOut); after != before {
			t.Fatalf("THE TRACKED ARTIFACT %s WAS OVERWRITTEN by this build (%s -> %s). Every build in this file passes -o; restore it with `git checkout -- %s`.", defaultOut, before[:12], after[:12], defaultOut)
		}
	}
	return out
}

// runRows runs a named set of top-level tests in a clone and returns their
// verdicts.
//
// PROOF OF EXECUTION. It FAILS unless every requested row reports a verdict by
// name. Without that, a clone that would not compile, or a `-run` pattern that
// matched nothing, returns an empty map that reads exactly like "no answer
// changed" — the vacuity this whole file is written against. `go test` exits 0
// on "no tests to run", so the exit code cannot be trusted for this.
func runRows(t *testing.T, pkgDir string, rows []string) (map[string]string, string) {
	t.Helper()
	pattern := "^(" + strings.Join(rows, "|") + ")$"
	out, _ := goTool(t, pkgDir, "test", "-v", "-count=1", "-run", pattern, ".")

	verdicts := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		for _, v := range []string{"PASS", "FAIL", "SKIP"} {
			prefix := "--- " + v + ": "
			if !strings.HasPrefix(s, prefix) {
				continue
			}
			name := strings.TrimPrefix(s, prefix)
			if i := strings.Index(name, " "); i >= 0 {
				name = name[:i]
			}
			if strings.Contains(name, "/") {
				continue // a subtest; the row's own verdict is the top-level line
			}
			verdicts[name] = v
		}
	}
	for _, row := range rows {
		if _, ok := verdicts[row]; !ok {
			t.Fatalf("PROOF OF EXECUTION: %s reported no verdict in the clone at %s.\nIt did not run, so nothing below is a measurement of it. `go test` exits 0 when its -run pattern matches nothing, so this is checked by name and not by exit code.\n%s", row, pkgDir, out)
		}
	}
	return verdicts, out
}

// scanPackageSource reports how many .go files in this package were read and
// which of them contain needle.
//
// This file is skipped: it names the digest of record, and a leg asserting that
// the digest is pinned in source must not be satisfied by the seal that asks
// for it. (It also writes the digest in two halves, so the contiguous literal
// is genuinely absent — the skip is belt to that braces.)
func scanPackageSource(t *testing.T, needle string) (int, []string) {
	t.Helper()
	const self = "baseline_seal_test.go"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == self {
			continue
		}
		data, err := os.ReadFile(name) // #nosec G304 -- a .go file in this package's own directory
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		if strings.Contains(string(data), needle) {
			hits = append(hits, name)
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no .go files — the scan is broken, not the source")
	}
	return scanned, hits
}

// `occurrences` lived here. Its only caller was row 5's main.go:403 patch, which
// the P4 (B1-rebuild) deleted when it inverted that row onto the frozen v1
// producer — a pinned fixture needs no anchor counted. Removed rather than left
// dark: an uncalled helper is what `unused` reports and what the next reader
// mistakes for machinery in service. patchSource below is still live (rows 4,
// 6a, 6b) and does its own exactly-once check.

// patchSource rewrites one unique literal in a source file inside a clone.
//
// It insists the anchor appears EXACTLY ONCE. A mutation applied to an anchor
// that has moved, or that now matches two places, is a mutation whose effect
// nobody knows, and a row resting on it would be measuring something it cannot
// name.
func patchSource(t *testing.T, path, old, replacement string) {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a source file in this test's own scratch clone
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), old); n != 1 {
		t.Fatalf("the mutation anchor %q occurs %d times in %s, want exactly 1 — re-derive it before trusting any row that rests on it", old, n, path)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, replacement, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sameSizeInertMutant returns bytes that differ from the file at path in
// exactly one byte, are the same length, and behave identically when executed.
//
// The candidate offsets are the middles of the image's longest runs of NUL
// padding. INERTNESS IS PROVEN BY EXECUTION, not inferred from the offset: each
// candidate is written out, run over a real fixture, and compared to the
// original's output. The first one that answers identically is returned; if
// none does, this RAISES rather than returning a mutant whose difference the
// differential could legitimately notice.
func sameSizeInertMutant(t *testing.T, path string) ([]byte, int) {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- the differential's reference, or a copy of it in a clone
	if err != nil {
		t.Fatal(err)
	}
	want := executableAnswer(t, path)

	type run struct{ start, length int }
	var runs []run
	for i := 0; i < len(data); {
		if data[i] != 0 {
			i++
			continue
		}
		j := i
		for j < len(data) && data[j] == 0 {
			j++
		}
		if j-i >= 512 {
			runs = append(runs, run{i, j - i})
		}
		i = j
	}
	sort.Slice(runs, func(a, b int) bool { return runs[a].length > runs[b].length })
	if len(runs) == 0 {
		t.Fatalf("%s has no run of NUL padding of 512 bytes or more; this mutation strategy does not apply to it and a new one must be derived rather than skipped", path)
	}

	scratch := filepath.Join(t.TempDir(), "mutant")
	for _, r := range runs {
		off := r.start + r.length/2
		mutant := append([]byte(nil), data...)
		if mutant[off] != 0 {
			continue
		}
		mutant[off] = 0x01
		if err := os.WriteFile(scratch, mutant, 0o700); err != nil { // #nosec G306 -- an executable in this test's own temp dir
			t.Fatal(err)
		}
		if len(mutant) != len(data) {
			t.Fatalf("the mutant is %d bytes and the original is %d — a same-size substitution is the whole point", len(mutant), len(data))
		}
		if executableAnswer(t, scratch) == want {
			return mutant, off
		}
	}
	t.Fatalf("no single-byte flip in %d NUL-padding runs of %s left its behaviour unchanged.\nThis row needs a substitute that a behavioural check cannot see; derive one rather than weakening the row.", len(runs), path)
	return nil, 0
}

// executableAnswer runs a v1 classify producer over a fixture and returns its
// normalised output, for use as an equivalence witness.
func executableAnswer(t *testing.T, bin string) string {
	t.Helper()
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	f := sealFixtures()[0]
	diffPath := filepath.Join(t.TempDir(), "fixture.diff")
	if err := os.WriteFile(diffPath, []byte(f.diffText()), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runLive(t, bin, pkgDir, nil, "-json", "-no-git", "-config", f.ConfigPath, diffPath)
	return fmt.Sprintf("exit=%d\n%s\n%s", r.exit, normaliseVolatile(r.stdout), r.stderr)
}

// classifiedAtStamp matches the one field in classify's output that is a clock
// rather than a behaviour.
//
// readset_seal_test.go's normaliseClassifiedAt is the equivalent for v1 JSON,
// but it FAILS when the field is absent — correctly, for its own callers, which
// are always looking at a v1 payload. The batteries here deliberately include
// invocations that produce no payload at all (the capability probe, the usage
// text, the INVALID_INPUT report), so they need a normaliser for which absence
// is an ordinary answer.
var classifiedAtStamp = regexp.MustCompile(`"classified_at":\s*"[^"]*"`)

func normaliseVolatile(s string) string {
	return classifiedAtStamp.ReplaceAllString(s, `"classified_at": "<VOLATILE>"`)
}

// ─── the drift fingerprint ───────────────────────────────────────────────────

// probeCase is one invocation in a drift battery.
type probeCase struct {
	name string
	env  []string
	args []string
}

// classifyProbeCases is the battery the classify drift guard compares over.
//
// It is chosen to span the surfaces a source fix would move: the capability
// probe (current-source-only), the v1 payload on a money path and on a scaffold
// table, the INVALID_INPUT report, the usage text, and — the one that matters
// most for this repo right now — the config search order under
// $RISK_PATHS_CONFIG, which is the repair the tree is carrying and the artifact
// is not.
func classifyProbeCases(t *testing.T) []probeCase {
	t.Helper()
	dir := t.TempDir()
	wallet := filepath.Join(dir, "wallet.diff")
	if err := os.WriteFile(wallet, []byte(diffFor(walletPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(dir, "docs.diff")
	if err := os.WriteFile(docs, []byte(diffFor("README.md")), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := writeDual(t, realTable(t), nil)
	attacker := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(attacker, driftedTable(t), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty-worktree")
	if err := os.MkdirAll(empty, 0o750); err != nil {
		t.Fatal(err)
	}

	return []probeCase{
		{name: "capability probe", args: []string{probeSubcommand}},
		{name: "usage", args: []string{"-nosuchflag"}},
		{name: "v1 money path", args: []string{"-json", "-no-git", "-config", exampleConfigPath, wallet}},
		{name: "v1 scaffold table", args: []string{"-json", "-no-git", "-config", scaffoldConfigPath, wallet}},
		{name: "v1 docs only", args: []string{"-json", "-no-git", "-config", exampleConfigPath, docs}},
		{name: "no config anywhere", args: []string{"-json", "-no-git", "-worktree", empty, wallet}},
		{name: "config search order under $RISK_PATHS_CONFIG",
			env:  []string{"RISK_PATHS_CONFIG=" + attacker},
			args: []string{"-json", "-no-git", "-worktree", wt, wallet}},
	}
}

// gatesProbeCases is the battery the gates drift guard compares over.
//
// The second case is the RECORDING'S OWN INVOCATION
// (TestSeal_Recorded_V1ProjectionDoesNotSurviveGates), run over a run-state
// seeded exactly the way that row seeds it. That is deliberate: the residual is
// specifically that the recording's subject and its source could part company,
// so the guard's battery has to contain the recording's own question.
func gatesProbeCases(t *testing.T) []probeCase {
	t.Helper()
	runState := filepath.Join(t.TempDir(), "run.json")
	seedRunState(t, runState)
	return []probeCase{
		{name: "usage", args: []string{"-h"}},
		{name: "the recording's own invocation", args: []string{
			"-run-state", runState,
			"-config", filepath.Join("testdata", "example-gates.json"),
			"-only", "nosuchgate",
		}},
	}
}

// fingerprint runs a battery against one binary and renders the answers as
// comparable text.
//
// Everything that names WHERE something is, rather than WHAT it does, is
// normalised out: the binary's own path (argv[0] appears in usage text), the
// temp directories the fixtures live in, and the classified_at clock. What
// remains is behaviour, which is what a drift guard is entitled to compare
// across two builds at two paths.
//
// The run-state case is run against a private copy for each binary, because
// cmd/gates rewrites the file it is given; comparing two binaries over one
// mutable file would let the first one decide the second one's answer.
func fingerprint(t *testing.T, bin, dir string, cases []probeCase) string {
	t.Helper()
	var b strings.Builder
	for _, c := range cases {
		args, subs := privatiseRunState(t, c.args)
		r := runLive(t, bin, dir, c.env, args...)
		out := fmt.Sprintf("exit=%d\n--stdout--\n%s\n--stderr--\n%s", r.exit, r.stdout, r.stderr)
		// The binary's own path only: NOT its basename. "classify" is a
		// substring of "cmd/classify", which the capability probe prints as a
		// value, and blanking it would manufacture a difference between two
		// builds that answered identically.
		out = strings.ReplaceAll(out, bin, "<BIN>")
		for from, to := range subs {
			out = strings.ReplaceAll(out, from, to)
		}
		out = strings.ReplaceAll(out, os.TempDir(), "<TMP>")
		out = normaliseVolatile(out)
		fmt.Fprintf(&b, "═══ %s\n%s\n", c.name, out)
	}
	return b.String()
}

// privatiseRunState gives each fingerprinted binary its own copy of any
// -run-state argument, and returns the substitutions that make the two copies
// compare as one path.
func privatiseRunState(t *testing.T, args []string) ([]string, map[string]string) {
	t.Helper()
	out := append([]string(nil), args...)
	subs := map[string]string{}
	for i := 0; i < len(out)-1; i++ {
		if out[i] != "-run-state" {
			continue
		}
		src := out[i+1]
		data, err := os.ReadFile(src) // #nosec G304 -- a run-state this test seeded
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(t.TempDir(), "run.json")
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			t.Fatal(err)
		}
		out[i+1] = dst
		subs[dst] = "<RUNSTATE>"
	}
	return out, subs
}

// firstDifference renders where two fingerprints part company, so a drift
// failure names the invocation that disagreed instead of printing two walls of
// output for a human to diff by eye.
func firstDifference(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	section := "(before any case header)"
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if strings.HasPrefix(wl, "═══ ") {
			section = strings.TrimPrefix(wl, "═══ ")
		}
		if gl != wl {
			return fmt.Sprintf("  first disagreement in case %q, line %d:\n    committed/left:  %q\n    fresh build:     %q", section, i+1, gl, wl)
		}
	}
	return "  (no textual difference found — the comparison itself is suspect)"
}
