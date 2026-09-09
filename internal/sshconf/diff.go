package sshconf

import (
	"fmt"
	"strings"
)

// UnifiedDiff renders a unified diff with three lines of context. It is meant
// for a human reading a confirmation prompt, not for feeding to patch.
func UnifiedDiff(path string, old, new []byte) string {
	if string(old) == string(new) {
		return ""
	}
	a := strings.Split(strings.TrimSuffix(string(old), "\n"), "\n")
	b := strings.Split(strings.TrimSuffix(string(new), "\n"), "\n")
	if len(old) == 0 {
		a = nil
	}
	if len(new) == 0 {
		b = nil
	}

	ops := diffLines(a, b)

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", path, path)

	const ctx = 3
	i := 0
	for i < len(ops) {
		if ops[i].kind == opEqual {
			i++
			continue
		}
		// Grow a hunk around this change, absorbing nearby ones.
		start := i
		for start > 0 && ops[start-1].kind == opEqual && start > i-ctx {
			start--
		}
		end := i
		for end < len(ops) {
			if ops[end].kind != opEqual {
				end++
				continue
			}
			run := 0
			for end+run < len(ops) && ops[end+run].kind == opEqual {
				run++
			}
			if run > 2*ctx || end+run >= len(ops) {
				end += min(run, ctx)
				break
			}
			end += run
		}

		aStart, bStart, aLen, bLen := 0, 0, 0, 0
		for k := 0; k < start; k++ {
			if ops[k].kind != opInsert {
				aStart++
			}
			if ops[k].kind != opDelete {
				bStart++
			}
		}
		for k := start; k < end; k++ {
			if ops[k].kind != opInsert {
				aLen++
			}
			if ops[k].kind != opDelete {
				bLen++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart+1, aLen, bStart+1, bLen)
		for k := start; k < end; k++ {
			switch ops[k].kind {
			case opEqual:
				out.WriteString(" " + ops[k].text + "\n")
			case opDelete:
				out.WriteString("-" + ops[k].text + "\n")
			case opInsert:
				out.WriteString("+" + ops[k].text + "\n")
			}
		}
		i = end
	}
	return out.String()
}

type opKind int

const (
	opEqual opKind = iota
	opDelete
	opInsert
)

type diffOp struct {
	kind opKind
	text string
}

// diffLines computes a line diff. Common prefix and suffix are stripped first,
// which keeps the quadratic table small for the edits tram actually makes.
func diffLines(a, b []string) []diffOp {
	var ops []diffOp

	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		ops = append(ops, diffOp{opEqual, a[p]})
		p++
	}
	sa, sb := a[p:], b[p:]

	s := 0
	for s < len(sa) && s < len(sb) && sa[len(sa)-1-s] == sb[len(sb)-1-s] {
		s++
	}
	tail := sa[len(sa)-s:]
	sa, sb = sa[:len(sa)-s], sb[:len(sb)-s]

	ops = append(ops, lcsOps(sa, sb)...)
	for _, t := range tail {
		ops = append(ops, diffOp{opEqual, t})
	}
	return ops
}

// lcsOps is a standard longest-common-subsequence diff over the middle region.
func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	if n == 0 {
		ops := make([]diffOp, m)
		for i, t := range b {
			ops[i] = diffOp{opInsert, t}
		}
		return ops
	}
	if m == 0 {
		ops := make([]diffOp, n)
		for i, t := range a {
			ops[i] = diffOp{opDelete, t}
		}
		return ops
	}

	tbl := make([][]int, n+1)
	for i := range tbl {
		tbl[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				tbl[i][j] = tbl[i+1][j+1] + 1
			} else {
				tbl[i][j] = max(tbl[i+1][j], tbl[i][j+1])
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{opEqual, a[i]})
			i++
			j++
		case tbl[i+1][j] >= tbl[i][j+1]:
			ops = append(ops, diffOp{opDelete, a[i]})
			i++
		default:
			ops = append(ops, diffOp{opInsert, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{opDelete, a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{opInsert, b[j]})
	}
	return ops
}
