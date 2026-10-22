// Command quintvet runs quint-connect-go's analyzers (qanalysis): quintsim,
// quintweave and quintaction. Standalone:
//
//	quintvet ./...                                      # all three
//	quintvet -quintweave=false -quintaction.binding app.binding.yaml ./...
//
// or as go vet's tool, which runs it per package with the build's types:
//
//	go vet -vettool=$(which quintvet) ./...
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/casselc/quint-connect-go/qanalysis"
)

func main() { multichecker.Main(qanalysis.All...) }
