//go:build ignore

// Evidence implementation of the v4 tuple encoding. It is deliberately a
// Go-side byte encoding; do not use CONCAT_WS or connection collation.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

type field struct {
	typeTag byte
	value   *string
}

func canonical(fields ...field) []byte {
	out := binary.AppendUvarint(nil, uint64(len(fields)))
	for _, f := range fields {
		out = append(out, f.typeTag)
		if f.value == nil {
			out = append(out, 0) // explicit NULL; no length/payload follows
			continue
		}
		out = append(out, 1) // present, including present-but-empty
		b := []byte(*f.value)
		out = binary.AppendUvarint(out, uint64(len(b)))
		out = append(out, b...)
	}
	return out
}

func main() {
	a, b, c := "a", "b", "c"
	aPipeB, bPipeC := "a|b", "b|c"
	rows := [][]field{
		{{1, &aPipeB}, {1, &c}},
		{{1, &a}, {1, &bPipeC}},
		{{1, &a}, {1, nil}, {1, &b}},
		{{1, &a}, {1, &b}, {1, nil}},
	}
	for _, row := range rows {
		fmt.Println(hex.EncodeToString(canonical(row...)))
	}
}
