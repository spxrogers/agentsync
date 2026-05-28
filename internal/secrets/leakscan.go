package secrets

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// secretMarker matches a ${secret:K} reference only (not ${env:…}).
var secretMarker = regexp.MustCompile(`\$\{secret:[A-Za-z0-9._-]+\}`)

// secretGroup identifies one server/hook-event in the canonical (scope+kind+id),
// independent of intra-group position (arg index, env/header key). Re-reference
// can relocate a secret WITHIN a group (a shifted arg, a renamed env key), so
// the leak scan reasons per group, not per field — otherwise a legitimately
// shifted-but-restored secret would look like a missing slot.
type secretGroup struct{ scope, kind, id string }

func groupOf(loc secretFieldLoc) secretGroup {
	return secretGroup{scope: loc.scope, kind: loc.kind, id: loc.id}
}

// ResidualSecretCleartext is the fail-closed backstop for the dest->source write
// path. After ReReferenceCanonical has done its best to restore ${secret:…}
// placeholders, this scans the about-to-be-written canonical for a resolved
// secret that would still persist as CLEARTEXT into the source (a committed
// dotfiles repo) — the dangerous class re-reference alone cannot fully close,
// because by value it cannot tell "moved/rotated secret" from "deliberate
// non-secret literal edit." capture.Capture refuses to write when this returns
// anything (it errs toward refusing rather than guessing).
//
// `ingested` is the re-referenced canonical about to be written; `against` is
// the current templated source. Two leak shapes, evaluated per secretGroup:
//
//   - VALUE: a live vault secret value appears verbatim in any field of the
//     group — a secret moved into a field whose source counterpart is a literal,
//     so re-reference left it unmasked.
//   - SLOT: a ${secret:K} the source group referenced is ABSENT from the entire
//     ingested group — a rotated/edited secret value re-reference couldn't match
//     (its cleartext, or a now-unreferenced credential, would persist). A
//     legitimately *shifted* secret keeps its placeholder somewhere in the
//     group, so it does NOT trip this.
//
// Returns human-readable group descriptions (empty = safe to write).
func ResidualSecretCleartext(ingested, against *source.Canonical, sec, env Resolver) []string {
	if ingested == nil || against == nil {
		return nil
	}
	secretVals := sourceSecretValues(against, sec) // resolved value -> placeholder
	srcByLoc := make(map[secretFieldLoc]string)
	walkSecretFields(against, func(loc secretFieldLoc, s string) string {
		srcByLoc[loc] = s
		return s
	})
	// Per-group concatenation of the ingested fields, so a secret legitimately
	// SHIFTED within a group (its placeholder restored at a new position) is not
	// mistaken for one rotated away.
	groupText := map[secretGroup]string{}
	walkSecretFields(ingested, func(loc secretFieldLoc, s string) string {
		groupText[groupOf(loc)] += "\x00" + s
		return s
	})

	seen := map[secretGroup]bool{}
	var leaks []string
	flag := func(g secretGroup) {
		if !seen[g] {
			seen[g] = true
			leaks = append(leaks, describeGroup(g))
		}
	}

	walkSecretFields(ingested, func(loc secretFieldLoc, s string) string {
		g := groupOf(loc)
		src := srcByLoc[loc]
		// VALUE prong (field-local): a live vault secret value present in THIS
		// field that is NOT already part of the field's own source counterpart —
		// i.e. a known secret moved/embedded here. A literal the user already had
		// in source (even one that coincidentally equals another secret's value)
		// is pre-existing, not a new leak, so it must not be refused.
		for v := range secretVals {
			if strings.Contains(s, v) && !strings.Contains(src, v) {
				flag(g)
				return s
			}
		}
		// SLOT prong: the field's source counterpart was a ${secret:K} slot, the
		// ingested value still has that slot's literal SHAPE with non-placeholder
		// content (a rotation/edit to cleartext re-reference couldn't match — NOT
		// a trim that removes the slot, which leaves no cleartext), AND the
		// placeholder is gone from the whole group (so a secret legitimately
		// SHIFTED elsewhere in the group, or an unrelated server's secret on a
		// single-item write-back, isn't flagged).
		if s == "" || !strings.Contains(src, "${secret:") {
			return s
		}
		if fieldRetainsRotatedSecret(src, s) {
			for _, ph := range secretPlaceholders(src) {
				if !strings.Contains(groupText[g], ph) {
					flag(g)
					return s
				}
			}
		}
		return s
	})

	// Passthrough Extra is verbatim and NOT secret-walked (see
	// source.MCPServerSpec.Extra), so the walk above can't see it. Scan it
	// separately for the VALUE shape: a live vault secret value appearing in an
	// Extra field that its source counterpart did not already hold — a resolved
	// secret a native tool duplicated into an unmodeled field — must not be
	// persisted as cleartext.
	scanExtraResidual(ingested, against, "", secretVals, flag)
	return leaks
}

// scanExtraResidual flags any secretGroup whose ingested passthrough Extra holds
// a live vault secret value the source counterpart's Extra did not — the only
// way a resolved secret can reach the verbatim, non-walked Extra. Recurses the
// Project overlay so a project-scoped server is covered too.
func scanExtraResidual(ingested, against *source.Canonical, scope string, secretVals map[string]string, flag func(secretGroup)) {
	if ingested == nil {
		return
	}
	srcMCP, srcLSP := map[string]string{}, map[string]string{}
	if against != nil {
		for _, m := range against.MCPServers {
			srcMCP[m.ID] = joinExtraStrings(m.Server.Extra)
		}
		for _, l := range against.LSPServers {
			srcLSP[l.ID] = joinExtraStrings(l.Spec.Extra)
		}
	}
	scan := func(kind, id, srcExtra string, extra map[string]any) {
		var ss []string
		extraStrings(extra, &ss)
		for _, s := range ss {
			for v := range secretVals {
				if v != "" && strings.Contains(s, v) && !strings.Contains(srcExtra, v) {
					flag(secretGroup{scope: scope, kind: kind, id: id})
					return
				}
			}
		}
	}
	for _, m := range ingested.MCPServers {
		scan("mcp", m.ID, srcMCP[m.ID], m.Server.Extra)
	}
	for _, l := range ingested.LSPServers {
		scan("lsp", l.ID, srcLSP[l.ID], l.Spec.Extra)
	}
	if ingested.Project != nil {
		var ap *source.Canonical
		if against != nil {
			ap = against.Project
		}
		scanExtraResidual(ingested.Project, ap, "project", secretVals, flag)
	}
}

// extraStrings recursively collects every string leaf in a passthrough Extra
// value (string, slice element, or nested-map value).
func extraStrings(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case []any:
		for _, e := range t {
			extraStrings(e, out)
		}
	case map[string]any:
		for _, e := range t {
			extraStrings(e, out)
		}
	}
}

func joinExtraStrings(extra map[string]any) string {
	var ss []string
	extraStrings(extra, &ss)
	return strings.Join(ss, "\x00")
}

// fieldRetainsRotatedSecret reports whether `ingested` matches the literal
// SHAPE of the source template `src` (each ${secret:K} standing for any run)
// with at least one slot holding non-placeholder cleartext. That distinguishes
// a ROTATION/edit ("a=${secret:K}" -> "a=newtoken", shape kept, slot has
// cleartext) from a TRIM that removes the secret and its surrounding context
// ("a=${secret:K} b=x" -> "b=x", shape broken -> no match -> not a leak).
func fieldRetainsRotatedSecret(src, ingested string) bool {
	segs := secretMarker.Split(src, -1)
	if len(segs) < 2 {
		return false // no ${secret:} marker in the counterpart
	}
	var pat strings.Builder
	pat.WriteString("^")
	for i, seg := range segs {
		if i > 0 {
			pat.WriteString("(.*)")
		}
		pat.WriteString(regexp.QuoteMeta(seg))
	}
	pat.WriteString("$")
	rx, err := regexp.Compile(pat.String())
	if err != nil {
		return true // can't build the matcher; fail safe (refuse)
	}
	caps := rx.FindStringSubmatch(ingested)
	if caps == nil {
		return false // shape broken (trim/restructure) — no cleartext slot
	}
	for _, cap := range caps[1:] {
		// A slot is cleartext if, after removing any ${secret:…}/${env:…}
		// placeholders that re-reference restored, non-whitespace remains.
		if strings.TrimSpace(re.ReplaceAllString(cap, "")) != "" {
			return true
		}
	}
	return false
}

// secretPlaceholders extracts the ${secret:K} markers in s (ignores ${env:…}).
func secretPlaceholders(s string) []string {
	if !strings.Contains(s, "${secret:") {
		return nil
	}
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if len(m) >= 3 && m[1] == "secret" {
			out = append(out, m[0])
		}
	}
	return out
}

func describeGroup(g secretGroup) string {
	scope := ""
	if g.scope != "" {
		scope = g.scope + " "
	}
	return fmt.Sprintf("%s%s %q", scope, g.kind, g.id)
}
