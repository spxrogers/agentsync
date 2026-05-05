//go:build bdd

// Package bdd is the BDD (Behaviour Driven Development) test surface for
// agentsync. It loads the .feature files under features/ and runs each
// Scenario against a freshly-built agentsync binary in a hermetic tmpdir.
//
// Run with:
//
//	go test -tags=bdd -v ./test/bdd/...
//
// Run a single feature:
//
//	go test -tags=bdd -v -run=TestBDD ./test/bdd/... -- -t '@drift'
//
// The build tag keeps the suite out of plain `go test ./...` so the existing
// fast unit/integration suites stay snappy.
package bdd

import (
	"context"
	"flag"
	"os"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/colors"

	"github.com/spxrogers/agentsync/test/bdd/support"
)

var opts = godog.Options{
	Output: colors.Colored(os.Stdout),
	Format: "pretty",
	Paths:  []string{"features"},
	// Concurrency=1 keeps every scenario in its own tmpdir without subtle
	// cross-talk through shared module/registry state. The suite runs in
	// well under a minute already.
	Concurrency:   1,
	Strict:        true,
	StopOnFailure: false,
	Randomize:     -1, // make randomness reproducible across runs
}

func init() {
	godog.BindCommandLineFlags("godog.", &opts)
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

// TestBDD is the godog driver. Every Scenario in features/*.feature is
// realised here against the SUT binary.
func TestBDD(t *testing.T) {
	bin := support.BuildBinary(t)

	w := &support.World{}
	suite := godog.TestSuite{
		Name: "agentsync",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				return ctx, w.Reset(t, bin)
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				w.Cleanup()
				return ctx, nil
			})
			support.RegisterSteps(sc, w)
		},
		Options: &opts,
	}
	if status := suite.Run(); status != 0 {
		t.Fatalf("godog suite failed (status=%d)", status)
	}
}
