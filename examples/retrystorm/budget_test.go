package retrystorm

import (
	"context"
	"testing"

	"github.com/casselc/quint-connect-go/examples/retrystorm/retry"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

func budget(m retry.Mutation) *retry.Budget {
	return &retry.Budget{Ratio: BudgetRatio, Max: BudgetMax, Mutation: m}
}

// The budget's steps are transitions of model/retry.qnt, with its balance,
// and keep the amplification bound and the cap.
func TestBudgetConforms(t *testing.T) {
	qtest.RequireQuint(t)
	qtest.Record(t, "retrystorm.binding.yaml", func(*qobs.Recorder) { Burst(budget(retry.Faithful)) }).Expect(t, qtest.Conforms())
}

// Uncapped: the 100th deposit of the quiet period overfills the bucket.
func TestBudgetCatchesUncapped(t *testing.T) {
	qtest.RequireQuint(t)
	run := qtest.Record(t, "retrystorm.binding.yaml", func(*qobs.Recorder) { Burst(budget(retry.Uncapped)) })
	run.Expect(t, qtest.Mismatch("request"))
}

// DepositOnRetry: the first admitted retry leaves a balance the model does
// not have.
func TestBudgetCatchesDepositOnRetry(t *testing.T) {
	qtest.RequireQuint(t)
	run := qtest.Record(t, "retrystorm.binding.yaml", func(*qobs.Recorder) { Burst(budget(retry.DepositOnRetry)) })
	run.Expect(t, qtest.Mismatch("retry"))
}

// machine drives a budget with drawn bursts of requests and retries.
type machine struct{ b *retry.Budget }

func (m *machine) RuleRequests(tc hegel.TestCase) {
	for n := hegel.Draw(tc, hegel.Integers(1, 120)); n > 0; n-- {
		m.b.Deposit(context.Background())
	}
}

func (m *machine) RuleRetries(tc hegel.TestCase) {
	for n := hegel.Draw(tc, hegel.Integers(1, 15)); n > 0; n-- {
		m.b.Withdraw(context.Background())
	}
}

func hegelCfg() quinthegel.Config {
	return quinthegel.Config{Binding: "retrystorm.binding.yaml", Global: true, AllowEmpty: true}
}

// Hegel drives the faithful budget; every run satisfies the model.
func TestHegelBudgetConforms(t *testing.T) {
	qtest.RequireQuint(t)
	cfg := hegelCfg()
	cfg.Mode = quinthegel.After
	quinthegel.Test(t, cfg, func(ht *hegel.T, _ *quinthegel.Recorder) {
		hegel.RunStateful(ht, &machine{b: budget(retry.Faithful)}, hegel.WithStatefulStepCount(6))
	}, hegel.WithTestCases(20), hegel.WithDatabase(""))
}

// Hegel finds the Uncapped mutant and shrinks it to a run that overfills
// the bucket.
func TestHegelCatchesUncapped(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model runs quint ~100 times")
	}
	v := quinthegel.FindViolation(t, hegelCfg(), func(tc hegel.TestCase, _ *quinthegel.Recorder) {
		hegel.RunStateful(tc, &machine{b: budget(retry.Uncapped)}, hegel.WithStatefulStepCount(6))
	}, hegel.WithTestCases(50))
	if last := v.Last(); last.Action != "request" {
		t.Errorf("shrunk run ends with %s, want the request that overfills the bucket:\n%s", last.Action, v.Summary)
	}
}
