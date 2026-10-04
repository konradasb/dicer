// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package bytesize writes byte counts for people, in IEC units to at most one
// decimal place, so that every size Dicer prints reads the same: in a table,
// a message or an event.
package bytesize

import (
	"math"
	"strconv"
)

// units are the IEC units sizes are written in.
var units = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}

// Format writes n bytes in the largest unit that leaves it at least 1:
// "512 B", "1.5 GiB".
func Format(n int64) string {
	value, unit := scale(n, n)
	return number(value) + " " + unit
}

// FormatOf writes part of total in total's unit, so that the two read as
// one: "1.5 of 30.3 GiB".
func FormatOf(part, total int64) string {
	value, unit := scale(total, total)
	partValue, _ := scale(part, total)
	return number(partValue) + " of " + number(value) + " " + unit
}

// scale returns n in the largest unit that leaves reference at least 1, and
// that unit.
func scale(n, reference int64) (float64, string) {
	value, ref := float64(n), float64(reference)
	i := 0
	for ref >= 1024 && i < len(units)-1 {
		value /= 1024
		ref /= 1024
		i++
	}
	return value, units[i]
}

// number writes f to at most one decimal place, dropping a trailing ".0".
func number(f float64) string {
	return strconv.FormatFloat(math.Round(f*10)/10, 'f', -1, 64)
}
