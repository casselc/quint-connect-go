// This file is how Orchestrion learns which aspects to weave: quintgo's
// //quint:action and //quint:thread, and qenv/weave's simulated locks, wait
// groups and //qenv:go (no dd-trace-go integrations).

//go:build tools

package tools

import (
	_ "github.com/DataDog/orchestrion"
	_ "github.com/casselc/quint-connect-go/qenv/weave"
	_ "github.com/casselc/quint-connect-go/qobs"
)
