package main

import (
	"github.com/hiett/lightning/tools/listEntryNonNullablePatcher/listEntryNonNullablePatcher"

	"golang.org/x/tools/go/analysis/multichecker"
)

func main() {
	multichecker.Main(listEntryNonNullablePatcher.Analyzer)
}
