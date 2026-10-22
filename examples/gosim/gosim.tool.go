// This file is how gosim's translator (the fork's weave package) learns
// which aspects to weave into the simulated packages: qobs's //quint:action
// and //quint:thread, so the edgepublish publisher's annotations record
// steps, as they do under `orchestrion go test`. qenv/weave's aspects are
// not listed: gosim simulates sync and goroutines itself.

//go:build tools

package tools

import (
	_ "github.com/casselc/quint-connect-go/qobs"
)
