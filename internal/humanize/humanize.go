// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package humanize writes sizes, durations and counts for people, so that
// every one Dicer prints reads the same: in a table, a message or an event.
package humanize

import (
	"math"
	"strconv"
	"time"
)

// sizeUnits are the IEC units sizes are written in.
var sizeUnits = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}

// Bytes writes n bytes in the largest unit that leaves it at least 1, to at
// most one decimal place: "512 B", "1.5 GiB".
func Bytes(n int64) string {
	value, unit := scale(n, n)
	return number(value) + " " + unit
}

// BytesOf writes part of total in total's unit, so that the two read as one:
// "1.5 of 30.3 GiB".
func BytesOf(part, total int64) string {
	value, unit := scale(total, total)
	partValue, _ := scale(part, total)
	return number(partValue) + " of " + number(value) + " " + unit
}

// scale returns n in the largest unit that leaves reference at least 1, and
// that unit.
func scale(n, reference int64) (float64, string) {
	value, ref := float64(n), float64(reference)
	i := 0
	for ref >= 1024 && i < len(sizeUnits)-1 {
		value /= 1024
		ref /= 1024
		i++
	}
	return value, sizeUnits[i]
}

// number writes f to at most one decimal place, dropping a trailing ".0".
func number(f float64) string {
	return strconv.FormatFloat(math.Round(f*10)/10, 'f', -1, 64)
}

// Duration writes how long something took, to a precision a person cares
// about: to the millisecond under a second, the tenth of a second under a
// minute, and the second after: "829ms", "1.8s", "5m3s".
func Duration(d time.Duration) string {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

// Count writes n of thing, adding an s for any number but one: "1 vCPU",
// "4 vCPUs".
func Count[N ~int | ~int32 | ~int64](n N, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.FormatInt(int64(n), 10) + " " + thing + "s"
}
