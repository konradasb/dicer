// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer"
)

// maxSuggestions is how many names a "did you mean" offers at most.
const maxSuggestions = 3

// suggest adds to a not-found error for name the names, from those list
// offers, that it was most likely a slip for:
//
//	no instance "wbe" (did you mean web?)
//
// Any other error, or one with nothing close, is returned as it was.
func suggest(
	ctx context.Context, client *dicer.Client, list completer, name string, err error,
) error {
	if status.Code(err) != codes.NotFound || name == "" {
		return err
	}

	names, listErr := list(ctx, client, nil)
	if listErr != nil {
		return err
	}
	for i, n := range names {
		names[i] = completionValue(n)
	}

	matches := closeNames(name, names)
	if len(matches) == 0 {
		return err
	}

	return fmt.Errorf("%w (did you mean %s?)", err, orList(matches))
}

// closeNames returns the names within a few edits of name, or holding it or
// held by it, closest first.
func closeNames(name string, names []string) []string {
	type candidate struct {
		name     string
		distance int
	}

	// Allow about one edit per three characters, or a containing name.
	limit := max(1, len(name)/3)
	contained := func(a, b string) bool { return len(b) >= 3 && strings.Contains(a, b) }

	var candidates []candidate
	for _, n := range names {
		if n == name {
			continue
		}
		d := editDistance(strings.ToLower(name), strings.ToLower(n))
		if d > limit && !contained(n, name) && !contained(name, n) {
			continue
		}
		candidates = append(candidates, candidate{n, d})
	}

	slices.SortStableFunc(candidates, func(a, b candidate) int { return a.distance - b.distance })

	out := make([]string, 0, maxSuggestions)
	for _, c := range candidates[:min(len(candidates), maxSuggestions)] {
		out = append(out, c.name)
	}
	return out
}

// editDistance is the Levenshtein distance between a and b, counting a
// swap of two neighbours as one edit, since that is the commonest typo.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)

	// d[i][j] is the distance between ra[:i] and rb[:j].
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}

	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}

	return d[len(ra)][len(rb)]
}

// orList joins names as a person would offer them: "a", "a or b",
// "a, b or c".
func orList(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// withHint appends hint to a daemon's error with the given code: `instance
// "web" is running: stop it first or use -f`.
func withHint(err error, code codes.Code, hint string) error {
	if status.Code(err) != code {
		return err
	}
	return fmt.Errorf("%w: %s", err, hint)
}
