package main

import (
	"os"
	"testing"
)

func BenchmarkRenderInProcess(b *testing.B) {
	src, err := os.ReadFile("testdata/withdraw-fsm.d2")
	if err != nil {
		b.Fatal(err)
	}
	for _, layout := range []string{"elk", "dagre"} {
		b.Run(layout, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, _, err := renderInProcess("testdata/withdraw-fsm.d2", src, layout, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
