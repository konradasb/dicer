// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"math"
	"strings"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// barWidth is how many cells a usage bar spans.
const barWidth = 20

// resourceFields are the host's resources as info shows them, a field each:
// a bar for how full it is, how much is in use of how much there is and as a
// percentage, then how that limit was arrived at.
//
//	vCPU: ████░░░░░░░░░░░░░░░░  4 of 16          25%
//	      4 CPUs, 4× overcommit
func resourceFields(r *dicerdv1.GetResourcesResponse, p palette) []field {
	cpu, memory, disk := r.GetCpu(), r.GetMemory(), r.GetDisk()
	diskUsed := disk.GetTotalBytes() - disk.GetFreeBytes()

	type row struct {
		label       string
		used, limit int64
		amount      string
		note        string
	}
	rows := []row{
		{"vCPU", cpu.GetAllocated(), cpu.GetAllocatable(),
			fmt.Sprintf("%d of %d", cpu.GetAllocated(), cpu.GetAllocatable()), cpuLimit(cpu)},
		{"Memory", memory.GetAllocated(), memory.GetAllocatable(),
			sizeOf(memory.GetAllocated(), memory.GetAllocatable()), memoryLimit(memory)},
		{"Disk", diskUsed, disk.GetTotalBytes(),
			sizeOf(diskUsed, disk.GetTotalBytes()), size(disk.GetProvisionedBytes()) + " provisioned"},
	}

	// The amounts are padded to one width, so the percentages line up.
	width := 0
	for _, r := range rows {
		width = max(width, len(r.amount))
	}

	fields := make([]field, 0, len(rows))
	for _, r := range rows {
		bar := p.level(fraction(r.used, r.limit), usageBar(r.used, r.limit))
		fields = append(fields, field{r.label, []string{
			fmt.Sprintf("%s  %-*s  %s", bar, width, r.amount, percent(r.used, r.limit)),
			r.note,
		}})
	}
	return fields
}

// fraction is used as a fraction of limit, or 0 if there is no limit.
func fraction(used, limit int64) float64 {
	if limit <= 0 {
		return 0
	}
	return float64(used) / float64(limit)
}

// cpuLimit explains the vCPU limit: "4 CPUs, 4× overcommit".
func cpuLimit(cpu *dicerdv1.ResourceCapacity) string {
	limit := plural(cpu.GetHost(), "CPU")
	if cpu.GetOvercommit() != 1 {
		limit += ", " + formatNumber(cpu.GetOvercommit()) + "× overcommit"
	}

	return limit
}

// memoryLimit explains the memory limit: "31.3 GiB, 1 GiB reserved".
func memoryLimit(memory *dicerdv1.ResourceCapacity) string {
	limit := size(memory.GetHost())
	if memory.GetReserved() > 0 {
		limit += ", " + size(memory.GetReserved()) + " reserved"
	}
	if memory.GetOvercommit() != 1 {
		limit += ", " + formatNumber(memory.GetOvercommit()) + "× overcommit"
	}

	return limit
}

// usageBar draws how much of limit is used. Any use shows at least one cell.
func usageBar(used, limit int64) string {
	filled := 0
	if limit > 0 {
		filled = int(math.Round(float64(min(used, limit)) / float64(limit) * barWidth))
	}
	if used > 0 && filled == 0 {
		filled = 1
	}

	return strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
}

// percent renders used as a percentage of limit, e.g. "25%".
func percent(used, limit int64) string {
	if limit <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", used*100/limit)
}

// plural renders a count of things, e.g. "1 CPU", "4 CPUs".
func plural(n int64, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}
