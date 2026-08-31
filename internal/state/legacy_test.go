package state

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// TestParseLegacyKey covers every v1 key shape that appears in this repo's own
// test corpus plus the adversarial ones. The v1 format joined fields with ':'
// and both the project root and the destination path may legally contain one,
// so the parser enumerates readings and accepts one only when the choice is
// forced.
func TestParseLegacyKey(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		pointered bool
		want      Key
		wantErr   bool
		ambiguous bool
	}{
		{
			name: "user scope, whole file",
			in:   "claude:user::${HOME}/CLAUDE.md",
			want: Key{Agent: "claude", Scope: "user", Path: "${HOME}/CLAUDE.md"},
		},
		{
			name: "user scope, empty path",
			in:   "claude:user::",
			want: Key{Agent: "claude", Scope: "user"},
		},
		{
			name: "user scope, colon-bearing path in a file key is unambiguous",
			in:   "claude:user::a:b:c",
			want: Key{Agent: "claude", Scope: "user", Path: "a:b:c"},
		},
		{
			name: "agent name is bound exactly",
			in:   "claude2:user::${HOME}/.claude.json",
			want: Key{Agent: "claude2", Scope: "user", Path: "${HOME}/.claude.json"},
		},
		{
			name: "project scope, plain",
			in:   "claude:project:${HOME}/proj:${HOME}/proj/.mcp.json",
			want: Key{Agent: "claude", Scope: "project", Project: "${HOME}/proj", Path: "${HOME}/proj/.mcp.json"},
		},
		{
			name: "project scope, colon-bearing root resolves by containment",
			in:   "claude:project:/mnt/we:ird/proj:/mnt/we:ird/proj/.mcp.json",
			want: Key{Agent: "claude", Scope: "project", Project: "/mnt/we:ird/proj", Path: "/mnt/we:ird/proj/.mcp.json"},
		},
		{
			name: "project scope, the #227 sibling-collision root",
			in:   "claude:project:${HOME}/work/app:staging:${HOME}/work/app:staging/.mcp.json",
			want: Key{
				Agent: "claude", Scope: "project",
				Project: "${HOME}/work/app:staging", Path: "${HOME}/work/app:staging/.mcp.json",
			},
		},
		{
			name: "project scope, single candidate wins even without containment",
			in:   "claude:project:${HOME}/proj:${HOME}/other/x.md",
			want: Key{Agent: "claude", Scope: "project", Project: "${HOME}/proj", Path: "${HOME}/other/x.md"},
		},
		{
			name:      "pointer key, user scope",
			in:        "claude:user::${HOME}/.claude.json:/mcpServers/github",
			pointered: true,
			want:      Key{Agent: "claude", Scope: "user", Path: "${HOME}/.claude.json", Pointer: "/mcpServers/github"},
		},
		{
			name:      "pointer key, colon-bearing path anchors on \":/\"",
			in:        "claude:user::a:b:/realptr",
			pointered: true,
			want:      Key{Agent: "claude", Scope: "user", Path: "a:b", Pointer: "/realptr"},
		},
		{
			name:      "pointer key, project scope",
			in:        "claude:project:${HOME}/proj:${HOME}/proj/.claude/settings.json:/hooks/PreToolUse",
			pointered: true,
			want: Key{
				Agent: "claude", Scope: "project", Project: "${HOME}/proj",
				Path: "${HOME}/proj/.claude/settings.json", Pointer: "/hooks/PreToolUse",
			},
		},
		// Windows shapes. paths.HomeRelative stores a path INSIDE $HOME as
		// "${HOME}/..." with forward slashes, but returns a path OUTSIDE it
		// VERBATIM — on Windows that keeps the drive colon and the backslashes.
		// Such a root therefore has several candidate ':' splits and only the
		// containment tie-break can settle them, so containmentParts must treat '\'
		// as a separator too. parseLegacyKey is pure string work, so these rows
		// exercise the Windows encoding from the Linux container.
		{
			name: "windows drive-letter project root outside %USERPROFILE%",
			in:   `claude:project:C:\dev\repo:C:\dev\repo\.mcp.json`,
			want: Key{
				Agent: "claude", Scope: "project",
				Project: `C:\dev\repo`, Path: `C:\dev\repo\.mcp.json`,
			},
		},
		{
			name: "windows project root on another drive",
			in:   `claude:project:D:\work\repo:D:\work\repo\.mcp.json`,
			want: Key{
				Agent: "claude", Scope: "project",
				Project: `D:\work\repo`, Path: `D:\work\repo\.mcp.json`,
			},
		},
		{
			name:      "windows drive-letter root, pointer key",
			in:        `claude:project:C:\dev\repo:C:\dev\repo\.claude\settings.json:/hooks/PreToolUse`,
			pointered: true,
			want: Key{
				Agent: "claude", Scope: "project", Project: `C:\dev\repo`,
				Path: `C:\dev\repo\.claude\settings.json`, Pointer: "/hooks/PreToolUse",
			},
		},
		{
			// A Windows project root INSIDE %USERPROFILE% is stored ${HOME}-relative
			// with forward slashes and no drive colon, so it was never affected —
			// pinned so the claim above is checked rather than asserted.
			name: "windows project root inside %USERPROFILE% is slash-form",
			in:   "claude:project:${HOME}/dev/repo:${HOME}/dev/repo/.mcp.json",
			want: Key{
				Agent: "claude", Scope: "project",
				Project: "${HOME}/dev/repo", Path: "${HOME}/dev/repo/.mcp.json",
			},
		},
		{
			// The separator fix is a TIE-BREAK, not a guess: a drive-letter root
			// whose destination is not under it still has no unique reading and is
			// still refused.
			name:      "windows drive-letter root with a dest outside it stays ambiguous",
			in:        `claude:project:C:\dev\repo:C:\other\x.md`,
			wantErr:   true,
			ambiguous: true,
		},
		// Dot-segment forgery. The containment tie-break used to normalize with
		// path.Clean, which COLLAPSES ".." — so a root or destination carrying a
		// ".." component lost components the other did not, the TRUE reading fell
		// out of the contained set, and a false reading was left alone in it and
		// won silently under the WRONG project. containmentParts leaves ".."
		// alone, so the true reading is always contained and the worst a false
		// one can do is force a refusal. The last two rows show the enabler is
		// the ".." collapse itself, not the '\' substitution: the family occurs
		// with backslashes, with only backslashes, and with none at all.
		{
			name: "dot segments do not forge a wrong-project reading",
			in:   `claude:project:H/\..:H/\../..:`,
			want: Key{Agent: "claude", Scope: "project", Project: `H/\..`, Path: `H/\../..:`},
		},
		{
			name: "dot segments with backslash separators only",
			in:   `claude:project:H\..:H\..\..:`,
			want: Key{Agent: "claude", Scope: "project", Project: `H\..`, Path: `H\..\..:`},
		},
		{
			name: "dot segments forge with no backslash at all",
			in:   "claude:project:H/..:H/../..:",
			want: Key{Agent: "claude", Scope: "project", Project: "H/..", Path: "H/../..:"},
		},
		{
			// Same family in a pointer key, where the forged reading swapped the
			// path/pointer split rather than the project. Two readings are now
			// genuinely contained, so it refuses instead of guessing.
			name:      "dot segments in a pointer key refuse rather than guess",
			in:        "claude:project:H:H/..:/a:/b",
			pointered: true,
			wantErr:   true,
			ambiguous: true,
		},
		{
			// Pins newProjectRoot's empty-project guard: without it the "" root
			// vacuously contains every relative path, so this tie would be
			// settled silently by the reading whose project field is empty.
			name:      "project scope, empty project field with a tie is refused",
			in:        "claude:project::a:b/x",
			wantErr:   true,
			ambiguous: true,
		},
		{
			// paths.HomeRelative stores the project root as "${HOME}/." when the
			// root IS $HOME (filepath.Rel(h, h) == "."), while every destination
			// under it is stored "${HOME}/<rel>". The two only share a component
			// prefix because containmentParts DROPS ".", so this row is what makes
			// that drop load-bearing: with `c == ""` alone the root normalizes to
			// ["${HOME}", "."], nothing under it is contained, and the tie below
			// is refused instead of settled.
			name: "project root of ${HOME} is stored ${HOME}/. and still contains its tree",
			in:   "claude:project:${HOME}/.:${HOME}/we:ird.mcp.json",
			want: Key{
				Agent: "claude", Scope: "project",
				Project: "${HOME}/.", Path: "${HOME}/we:ird.mcp.json",
			},
		},
		{
			// A zero-component root ("/", and the "\", "." and "./." forms a
			// stored key cannot actually hold) contains every path of matching
			// rootedness, so a FALSE split of "/" pads the contained set and costs
			// an acceptance here. That over-refusal is DELIBERATE: excluding such
			// roots would recover this key but silently misdecode a key whose true
			// root is "/" — see projectRoot.contains. The row exists so the cost
			// is visible and the cure cannot be applied without breaking a test.
			name:      "a false zero-component root pads the tie and costs an acceptance",
			in:        "claude:project:/:/a:/:/a/AGENTS.md",
			wantErr:   true,
			ambiguous: true,
		},
		{
			// The same shape with the roles swapped: here "/" is the TRUE root and
			// "/://" is the false split that containment also accepts. Refusing is
			// right; decoding it under "/://" (which is what excluding
			// zero-component roots does) is the wrong-project bug this format
			// change exists to remove.
			name:      "a true zero-component root is never traded for a false discriminating one",
			in:        "claude:project:/://:/:",
			wantErr:   true,
			ambiguous: true,
		},
		{
			name:    "user scope with a non-empty project field",
			in:      "claude:user:proj:${HOME}/x.md",
			wantErr: true,
		},
		{
			name:    "project scope with no ':' in the remainder",
			in:      "claude:project:onlyproject",
			wantErr: true,
		},
		{
			name: "project scope with an empty project field",
			in:   "claude:project::${HOME}/x.md",
			want: Key{Agent: "claude", Scope: "project", Path: "${HOME}/x.md"},
		},
		{
			name:    "no scope field",
			in:      "opencode:user",
			wantErr: true,
		},
		{
			name:    "second field is not a scope",
			in:      "claude:nope::x",
			wantErr: true,
		},
		{
			name:    "no agent field",
			in:      ":user::x",
			wantErr: true,
		},
		{
			name:      "pointer key with no pointer",
			in:        "claude:user::${HOME}/.claude.json",
			pointered: true,
			wantErr:   true,
		},
		{
			name:      "ambiguous project split",
			in:        "claude:project:${HOME}/a:${HOME}/b:${HOME}/c",
			wantErr:   true,
			ambiguous: true,
		},
		{
			name:      "ambiguous pointer split",
			in:        "claude:user::a:/b:/c",
			pointered: true,
			wantErr:   true,
			ambiguous: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parse := parseLegacyFileKey
			if tc.pointered {
				parse = parseLegacyPointerKey
			}
			got, err := parse(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parse(%q) should fail; got %+v", tc.in, got)
				}
				if tc.ambiguous && !errors.Is(err, errAmbiguousLegacyKey) {
					t.Fatalf("parse(%q) error should be errAmbiguousLegacyKey; got %v", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parse(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseLegacyKey_ReadingCapBoundsWork pins maxLegacyReadings.
//
// parseLegacyKey's enumeration is a PRODUCT — every ':' in the remainder is a
// candidate project/tail boundary and every ":/" in each of those tails is a
// candidate path/pointer boundary — and the containment tie-break then walks the
// result, so the work is cubic in the key's ":/" count. targets.json is
// hand-editable (migrate's own remedy invites it) and is read by status, apply,
// diff and doctor, so an uncapped key was a denial of service: measured before
// the cap, a 215-byte key cost 15 ms / 20 MiB, an 815-byte key 0.9 s / 1.3 GiB
// and a 1615-byte key 4.7 s / 10 GiB — an OOM kill a little above 4 KB.
//
// The assertion is on ALLOCATED BYTES, not a timeout: it is a real bound the
// uncapped implementation misses by roughly five orders of magnitude, and it
// does not turn a slow CI machine into a flake.
func TestParseLegacyKey_ReadingCapBoundsWork(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"pathological pointer key", "claude:project:" + strings.Repeat(":/", 800)},
		{"pathological pointer key, 40x larger", "claude:project:" + strings.Repeat(":/", 20000)},
		{"pathological file key", "claude:project:" + strings.Repeat(":", 1600)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Bound: 64 bytes per key byte plus 64 KiB of slack. Generous — the
			// capped parser measured ~3-7x the key length — but still four to five
			// orders of magnitude under what the uncapped parser allocated on the
			// same inputs.
			const perByte, slack = 64, 64 << 10
			want := uint64(perByte*len(tc.key) + slack)

			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)
			_, err := parseLegacyPointerKey(tc.key)
			runtime.ReadMemStats(&m1)

			if !errors.Is(err, errAmbiguousLegacyKey) {
				t.Fatalf("a key past the cap must refuse as ambiguous; got %v", err)
			}
			if !strings.Contains(err.Error(), "more than 64") {
				t.Errorf("the refusal must name the cap that fired; got %v", err)
			}
			if got := m1.TotalAlloc - m0.TotalAlloc; got > want {
				t.Fatalf("parsing a %d-byte key allocated %d bytes, want <= %d", len(tc.key), got, want)
			}
		})
	}
}

// legacyContainmentCorpus generates the (project root, destination) pairs
// TestLegacyContainmentProperties quantifies over: every destination is its root
// plus a separator plus a relative tail, which is exactly the shape an adapter's
// project-scope ResolvePaths produces.
//
// The alphabet is chosen for what it stresses, not for realism: an ordinary
// name, a bare ':' (the byte that makes the v1 encoding ambiguous at all), the
// '.' and '..' dot segments containmentParts respectively drops and deliberately
// does not collapse, a name embedding a ':', and ":/" (the pointer anchor). The
// prefixes cover a POSIX root, the "${HOME}/" form paths.HomeRelative emits, the
// `C:\` drive-letter form it passes through verbatim on Windows, and — as roots
// in their own right — the zero-component "/" and "${HOME}/." spellings.
func legacyContainmentCorpus(t *testing.T) [][2]string {
	t.Helper()
	comps := []string{"a", ":", ".", "..", "b:c", ":/"}
	prefixes := []string{"/", "${HOME}/", `C:\`}
	seps := []string{"/", `\`}

	seen := map[string]bool{}
	var roots []string
	addRoot := func(r string) {
		if !seen[r] {
			seen[r] = true
			roots = append(roots, r)
		}
	}
	for _, p := range prefixes {
		addRoot(p)
		for _, sep := range seps {
			for _, c1 := range comps {
				addRoot(p + c1)
				for _, c2 := range comps {
					addRoot(p + c1 + sep + c2)
				}
			}
		}
	}
	var tails []string
	for _, c1 := range comps {
		tails = append(tails, c1)
		for _, c2 := range comps {
			tails = append(tails, c1+"/"+c2)
		}
	}

	pairSeen := map[[2]string]bool{}
	var out [][2]string
	add := func(root, dest string) {
		pair := [2]string{root, dest}
		if !pairSeen[pair] {
			pairSeen[pair] = true
			out = append(out, pair)
		}
	}
	for _, root := range roots {
		for _, tail := range tails {
			for _, sep := range seps {
				add(root, root+sep+tail)
				// The one real shape where the destination is NOT byte-for-byte
				// root + separator + tail: when the project root IS $HOME,
				// paths.HomeRelative stores it as "${HOME}/." (filepath.Rel(h, h)
				// == ".") while the destination under it goes through
				// filepath.Join, which CLEANS the "." away — so the stored pair is
				// ("${HOME}/.", "${HOME}/<tail>"). Only containmentParts dropping
				// "." keeps the true reading contained here.
				if trimmed, ok := strings.CutSuffix(root, "/."); ok {
					add(root, trimmed+"/"+tail)
				}
			}
		}
	}
	return out
}

// TestLegacyContainmentProperties pins the invariant parseLegacyKey's step 7
// rests on, which lived only as prose.
//
// Every adapter writes a project-scope destination as the project root plus a
// relative tail, so the reading agentsync ACTUALLY WROTE is always contained.
// That is what makes containment safe as a tie-break: a false reading can only
// ADD to the contained set, so at worst it forces a refusal — it can never take
// the acceptance for itself. The property is universally quantified over key
// bytes, so it is checked over a generated corpus rather than tabulated; the
// rows in TestParseLegacyKey each pin one instance of it.
//
// Three sub-properties, in increasing strength. The third is the one that
// matters operationally: over-refusing a v1 key costs a documented, cheap
// re-adopt, but decoding one under the wrong project is issue #227 all over
// again.
func TestLegacyContainmentProperties(t *testing.T) {
	corpus := legacyContainmentCorpus(t)
	if len(corpus) < 1000 {
		t.Fatalf("corpus is too small to quantify over: %d pairs", len(corpus))
	}

	t.Run("the true reading is always contained", func(t *testing.T) {
		for _, pair := range corpus {
			root, dest := pair[0], pair[1]
			if !newProjectRoot(root).contains(dest) {
				t.Fatalf("root %q must contain the destination %q it was joined onto", root, dest)
			}
		}
	})

	// No true reading can exercise the rootedness arm — root and destination
	// always agree on it — so only a negative case pins it. Re-rooting the
	// destination leaves its component list untouched, so this arm is the only
	// thing that can reject it.
	t.Run("a destination of the other rootedness is never contained", func(t *testing.T) {
		for _, pair := range corpus {
			root, dest := pair[0], pair[1]
			r := newProjectRoot(root)
			flipped := "/" + dest
			if r.rooted {
				flipped = strings.TrimLeft(dest, `/\`)
			}
			if r.contains(flipped) {
				t.Fatalf("root %q (rooted=%v) must not contain %q", root, r.rooted, flipped)
			}
		}
	})

	t.Run("a decoded key never names a project other than the true root", func(t *testing.T) {
		const pointer = "/mcpServers/gh"
		var accepted int
		for _, pair := range corpus {
			root, dest := pair[0], pair[1]

			fileKey := "claude:project:" + root + ":" + dest
			got, err := parseLegacyFileKey(fileKey)
			if err == nil {
				accepted++
				if got.Project != root || got.Path != dest {
					t.Fatalf("%q decoded as project %q path %q, want %q / %q",
						fileKey, got.Project, got.Path, root, dest)
				}
			}

			ptrKey := fileKey + ":" + pointer
			got, err = parseLegacyPointerKey(ptrKey)
			if err == nil {
				accepted++
				if got.Project != root || got.Path != dest || got.Pointer != pointer {
					t.Fatalf("%q decoded as project %q path %q pointer %q, want %q / %q / %q",
						ptrKey, got.Project, got.Path, got.Pointer, root, dest, pointer)
				}
			}
		}
		// Refusing everything would satisfy the property VACUOUSLY, so pin a
		// floor. 70.7% of this deliberately adversarial corpus decodes today;
		// half is well clear of that, and still fails loudly if a change turns
		// the tie-break into a blanket refusal.
		total := 2 * len(corpus)
		if accepted < total/2 {
			t.Fatalf("only %d of %d generated keys decoded; the property is near-vacuous", accepted, total)
		}
	})
}
