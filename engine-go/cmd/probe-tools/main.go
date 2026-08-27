package main

import (
	"fmt"
	"os"

	"kibborg/engine/secops"
)

func main() {
	wd, _ := os.Getwd()
	fmt.Println("cwd=", wd)
	tools := secops.ProbeLocalTools()
	ok, miss := 0, 0
	for _, t := range tools {
		if t.OK {
			ok++
			fmt.Printf("OK\t%s\t%s\n", t.Name, t.Path)
		} else {
			miss++
			fmt.Printf("MISS\t%s\t%s\n", t.Name, t.Note)
		}
	}
	fmt.Printf("SUMMARY\tok=%d\tmiss=%d\ttotal=%d\n", ok, miss, len(tools))
	for k, v := range secops.DictionaryPaths() {
		fmt.Printf("DICT\t%s\t%s\n", k, v)
	}
	fmt.Println(secops.LocalToolsSummary(tools))
}
