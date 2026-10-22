// Package golangci registers qanalysis's analyzers as a golangci-lint
// module plugin named "quintgo". In .custom-gcl.yml:
//
//	plugins:
//	  - module: github.com/casselc/quint-connect-go/qanalysis
//	    import: github.com/casselc/quint-connect-go/qanalysis/golangci
//	    version: <version>
//
// and in .golangci.yml:
//
//	linters:
//	  enable: [quintgo]
//	  settings:
//	    custom:
//	      quintgo:
//	        type: module
//	        settings:
//	          analyzers: [quintsim, quintaction]   # default: all three
//	          binding: app.binding.yaml            # quintaction's -binding
package golangci

import (
	"fmt"
	"slices"

	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"

	"github.com/casselc/quint-connect-go/qanalysis"
)

func init() { register.Plugin("quintgo", New) }

// Settings are the plugin's settings in .golangci.yml.
type Settings struct {
	// Analyzers to run, by name (quintsim, quintweave, quintaction; empty:
	// all).
	Analyzers []string `json:"analyzers"`
	// Binding and Quint are quintaction's -binding and -quint.
	Binding string `json:"binding"`
	Quint   string `json:"quint"`
}

// New is the plugin's constructor.
func New(conf any) (register.LinterPlugin, error) {
	s, err := register.DecodeSettings[Settings](conf)
	if err != nil {
		return nil, err
	}
	for name, v := range map[string]string{"binding": s.Binding, "quint": s.Quint} {
		if v != "" {
			if err := qanalysis.Action.Flags.Set(name, v); err != nil {
				return nil, err
			}
		}
	}
	return &plugin{s}, nil
}

type plugin struct{ s Settings }

// BuildAnalyzers implements register.LinterPlugin.
func (p *plugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	if len(p.s.Analyzers) == 0 {
		return qanalysis.All, nil
	}
	var out []*analysis.Analyzer
	for _, name := range p.s.Analyzers {
		i := slices.IndexFunc(qanalysis.All, func(a *analysis.Analyzer) bool { return a.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("quintgo: no analyzer %q (have quintsim, quintweave, quintaction)", name)
		}
		out = append(out, qanalysis.All[i])
	}
	return out, nil
}

// GetLoadMode implements register.LinterPlugin: quintaction needs types.
func (p *plugin) GetLoadMode() string { return register.LoadModeTypesInfo }
