package main

// THE FROZEN v1 DIFFERENTIAL BASELINE.
//
// cmd/classify/classify was three things at once: the artifact production execs
// by absolute path (roles/tasker.md:224, skills/pr-raise.md:36, README.md:35),
// the pinned v1 differential baseline, and this module's default `go build`
// output path. The third job destroys the second. Measured, not argued:
//
//   - a same-size mutant of the reference — one byte flipped 0x00 -> 0x01 at
//     offset 2601969, behaviour proven identical by execution — left both
//     differential rows PASSING. The differential was checking presence, not
//     bytes;
//   - on a tree carrying a real divergence (risk_reasons -> risk_reasonz) the
//     bytes row was FAIL, and one `go build ./...` later it was PASS. The
//     rebuild did not fix the wire; it replaced the reference with the broken
//     subject, so the divergence had nobody left to disagree with.
//
// The reference therefore lives under testdata/, where no documented build
// writes, and its bytes are NAMED here rather than trusted from disk.
// cmd/classify/classify goes back to being an ordinary build output that may be
// rebuilt and committed freely.
//
// REPRODUCING THE ARTIFACT — it is a byte copy of a tracked blob, not a build:
//
//	git show 9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0 > cmd/classify/testdata/baseline/classify-v1-ad289891e9c7
//	chmod +x cmd/classify/testdata/baseline/classify-v1-ad289891e9c7

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// PinnedBaselineV1Path is the frozen v1 producer the differential measures
// against.
//
// It is under testdata/ because nothing builds there. `go build .`,
// `go build ./...` and `go build -o classify .` all write ./classify — that is
// what made ./classify unusable as a reference — and none of them can reach
// this path.
const PinnedBaselineV1Path = "testdata/baseline/classify-v1-ad289891e9c7"

// PinnedBaselineV1SHA256 is that file's sha256, written down IN SOURCE so a
// diff shows it changing.
//
// This is D4's TS_VENDORED_PARSER_SHA256 lesson, in two halves:
//
//   - a digest RECOMPUTED FROM THE FILE pins nothing. It agrees with whatever
//     bytes are on disk, including bytes nobody meant to be there, because it
//     is derived from the very thing it claims to pin;
//   - a `stat` cannot see a same-size substitution, so the check has to be a
//     hash. The measured mutant above keeps the size AND the behaviour; only a
//     comparison against a value written down somewhere else notices it.
//
// MEASURED:
//
//	$ git rev-parse HEAD:cmd/classify/classify
//	9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0
//	$ sha256sum cmd/classify/testdata/baseline/classify-v1-ad289891e9c7
//	ad289891e9c73306c8144cf8e50fd1e6662a02229f4c0d008fd87d1330cba28f
//
// MOVING THE BASELINE FORWARD IS AN OPERATOR DECISION, not maintenance. A
// baseline re-derived from a build of this tree makes the differential compare
// the source against itself, which looks exactly like the fix. Changing this
// value must name the new reference and say why it predates the tree.
const PinnedBaselineV1SHA256 = "ad289891e9c73306c8144cf8e50fd1e6662a02229f4c0d008fd87d1330cba28f"

// VerifyPinnedBaseline reports whether the file at path is the frozen v1
// producer, by hashing it and comparing against PinnedBaselineV1SHA256.
//
// CALL IT BEFORE EXECUTING THE REFERENCE, not after, and not once at startup:
// the differential's claim is about the bytes it actually ran, so the check
// belongs on the path that uses them.
//
// It takes a path rather than reading PinnedBaselineV1Path itself so the same
// check can be applied to a copy — a clone, a candidate replacement — without
// anyone reimplementing it. What it compares against is always the constant
// above. It never derives the expectation from the file it is checking; that
// mutation was built and confirmed to accept the same-size mutant.
func VerifyPinnedBaseline(path string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- the tracked baseline fixture, or a copy of it in a scratch clone
	if err != nil {
		return fmt.Errorf("the frozen v1 baseline %s is unreadable: %w\n"+
			"It is a tracked fixture, not a build output. Restore it with `git checkout -- %s`, or reproduce it byte for byte with `git show 9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0 > %s && chmod +x %s`",
			path, err, PinnedBaselineV1Path, PinnedBaselineV1Path, PinnedBaselineV1Path)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != PinnedBaselineV1SHA256 {
		return fmt.Errorf("THE FROZEN v1 BASELINE IS NOT THE PINNED BYTES.\n"+
			"  %s\n"+
			"  sha256 %s\n"+
			"  want   %s  (git blob 9542fe1ba7bcfada1ae2c2a53fa3499a4931edf0)\n"+
			"  size   %d bytes\n"+
			"The differential's reference is supposed to predate this tree. If it was re-derived from a build of the tree, the differential has quietly started comparing the source against itself. If the baseline was moved forward deliberately, that is an operator decision: update PinnedBaselineV1SHA256 in the same commit and say what the new reference is and why it predates the tree",
			path, got, PinnedBaselineV1SHA256, len(data))
	}
	return nil
}
