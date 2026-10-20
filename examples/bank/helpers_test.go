package bank

import "github.com/casselc/quint-connect-go/itf"

func readTrace(path string) (*itf.Trace, error) { return itf.ReadFile(path) }
