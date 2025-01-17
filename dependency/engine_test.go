// Copyright 2015 Canonical Ltd.
// Licensed under the LGPLv3, see LICENCE file for details.

package dependency_test

import (
	"time"

	"github.com/juju/clock"
	"github.com/juju/clock/testclock"
	"github.com/juju/errors"
	"github.com/juju/loggo"
	"github.com/juju/testing"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"

	"github.com/juju/worker/v3"
	"github.com/juju/worker/v3/dependency"
	"github.com/juju/worker/v3/workertest"
)

type EngineSuite struct {
	testing.IsolationSuite
	fix *engineFixture
}

var _ = gc.Suite(&EngineSuite{})

func (s *EngineSuite) SetUpTest(c *gc.C) {
	s.IsolationSuite.SetUpTest(c)
	s.fix = &engineFixture{}
	loggo.GetLogger("test").SetLogLevel(loggo.TRACE)
}

func (s *EngineSuite) TestInstallConvenienceWrapper(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {
		mh1 := newManifoldHarness()
		mh2 := newManifoldHarness()
		mh3 := newManifoldHarness()

		err := dependency.Install(engine, dependency.Manifolds{
			"mh1": mh1.Manifold(),
			"mh2": mh2.Manifold(),
			"mh3": mh3.Manifold(),
		})
		c.Assert(err, jc.ErrorIsNil)

		mh1.AssertOneStart(c)
		mh2.AssertOneStart(c)
		mh3.AssertOneStart(c)
	})
}

func (s *EngineSuite) TestInstallNoInputs(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Install a worker, check it starts.
		mh1 := newManifoldHarness()
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		// Install a second independent worker; check the first in untouched.
		mh2 := newManifoldHarness()
		err = engine.Install("other-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)
		mh1.AssertNoStart(c)
	})
}

func (s *EngineSuite) TestInstallUnknownInputs(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Install a worker with an unmet dependency, check it doesn't start
		// (because the implementation returns ErrMissing).
		mh1 := newManifoldHarness("later-task")
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertNoStart(c)

		// Install its dependency; check both start.
		mh2 := newManifoldHarness()
		err = engine.Install("later-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)
		mh1.AssertOneStart(c)
	})
}

func (s *EngineSuite) TestDoubleInstall(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Install a worker.
		mh := newManifoldHarness()
		err := engine.Install("some-task", mh.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh.AssertOneStart(c)

		// Can't install another worker with the same name.
		err = engine.Install("some-task", mh.Manifold())
		c.Assert(err, gc.ErrorMatches, `"some-task" manifold already installed`)
		mh.AssertNoStart(c)
	})
}

func (s *EngineSuite) TestInstallCycle(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Install a worker with an unmet dependency.
		mh1 := newManifoldHarness("robin-hood")
		err := engine.Install("friar-tuck", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertNoStart(c)

		// Can't install another worker that creates a dependency cycle.
		mh2 := newManifoldHarness("friar-tuck")
		err = engine.Install("robin-hood", mh2.Manifold())
		c.Assert(err, gc.ErrorMatches, `cannot install "robin-hood" manifold: cycle detected at .*`)
		mh2.AssertNoStart(c)
	})
}

func (s *EngineSuite) TestInstallAlreadyStopped(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Shut down the engine.
		err := worker.Stop(engine)
		c.Assert(err, jc.ErrorIsNil)

		// Can't start a new task.
		mh := newManifoldHarness()
		err = engine.Install("some-task", mh.Manifold())
		c.Assert(err, gc.ErrorMatches, "engine is shutting down")
		mh.AssertNoStart(c)
	})
}

func (s *EngineSuite) TestStartGetExistenceOnly(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start a task with a dependency.
		mh1 := newManifoldHarness()
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		// Start another task that depends on it, ourselves depending on the
		// implementation of manifoldHarness, which calls Get(foo, nil).
		mh2 := newManifoldHarness("some-task")
		err = engine.Install("other-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)
	})
}

func (s *EngineSuite) TestStartGetUndeclaredName(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Install a task and make sure it's started.
		mh1 := newManifoldHarness()
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		// Install another task with an undeclared dependency on the started task.
		done := make(chan struct{})
		err = engine.Install("other-task", dependency.Manifold{
			Start: func(context dependency.Context) (worker.Worker, error) {
				err := context.Get("some-task", nil)
				c.Check(errors.Cause(err), gc.Equals, dependency.ErrMissing)
				c.Check(err, gc.ErrorMatches, `"some-task" not declared: dependency not available`)
				close(done)
				// Return a real worker so we don't keep restarting and potentially double-closing.
				return startMinimalWorker(context)
			},
		})
		c.Assert(err, jc.ErrorIsNil)

		// Wait for the check to complete before we stop.
		select {
		case <-done:
		case <-time.After(testing.LongWait):
			c.Fatalf("dependent task never started")
		}
	})
}

func (s *EngineSuite) testStartGet(c *gc.C, outErr error) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start a task with an Output func that checks what it's passed, and wait for it to start.
		var target interface{}
		expectTarget := &target
		mh1 := newManifoldHarness()
		manifold := mh1.Manifold()
		manifold.Output = func(worker worker.Worker, target interface{}) error {
			// Check we got passed what we expect regardless...
			c.Check(target, gc.DeepEquals, expectTarget)
			// ...and return the configured error.
			return outErr
		}
		err := engine.Install("some-task", manifold)
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		// Start another that tries to use the above dependency.
		done := make(chan struct{})
		err = engine.Install("other-task", dependency.Manifold{
			Inputs: []string{"some-task"},
			Start: func(context dependency.Context) (worker.Worker, error) {
				err := context.Get("some-task", &target)
				// Check the result from some-task's Output func matches what we expect.
				c.Check(err, gc.Equals, outErr)
				close(done)
				// Return a real worker so we don't keep restarting and potentially double-closing.
				return startMinimalWorker(context)
			},
		})
		c.Check(err, jc.ErrorIsNil)

		// Wait for the check to complete before we stop.
		select {
		case <-done:
		case <-time.After(testing.LongWait):
			c.Fatalf("other-task never started")
		}
	})
}

func (s *EngineSuite) TestStartGetAccept(c *gc.C) {
	s.testStartGet(c, nil)
}

func (s *EngineSuite) TestStartGetReject(c *gc.C) {
	s.testStartGet(c, errors.New("not good enough"))
}

func (s *EngineSuite) TestStartAbortOnEngineKill(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {
		starts := make(chan struct{}, 1000)
		manifold := dependency.Manifold{
			Start: func(context dependency.Context) (worker.Worker, error) {
				starts <- struct{}{}
				select {
				case <-context.Abort():
				case <-time.After(testing.LongWait):
					c.Errorf("timed out")
				}
				return nil, errors.New("whatever")
			},
		}
		err := engine.Install("task", manifold)
		c.Assert(err, jc.ErrorIsNil)

		select {
		case <-starts:
		case <-time.After(testing.LongWait):
			c.Fatalf("timed out")
		}
		workertest.CleanKill(c, engine)

		select {
		case <-starts:
			c.Fatalf("unexpected start")
		default:
		}
	})
}

func (s *EngineSuite) TestStartAbortOnDependencyChange(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {
		starts := make(chan struct{}, 1000)
		manifold := dependency.Manifold{
			Inputs: []string{"parent"},
			Start: func(context dependency.Context) (worker.Worker, error) {
				starts <- struct{}{}
				select {
				case <-context.Abort():
				case <-time.After(testing.LongWait):
					c.Errorf("timed out")
				}
				return nil, errors.New("whatever")
			},
		}
		err := engine.Install("child", manifold)
		c.Assert(err, jc.ErrorIsNil)

		select {
		case <-starts:
		case <-time.After(testing.LongWait):
			c.Fatalf("timed out")
		}

		mh := newManifoldHarness()
		err = engine.Install("parent", mh.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh.AssertOneStart(c)

		select {
		case <-starts:
		case <-time.After(testing.LongWait):
			c.Fatalf("timed out")
		}
		workertest.CleanKill(c, engine)

		select {
		case <-starts:
			c.Fatalf("unexpected start")
		default:
		}
	})
}

func (s *EngineSuite) TestErrorRestartsDependents(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start two tasks, one dependent on the other.
		mh1 := newManifoldHarness()
		err := engine.Install("error-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		mh2 := newManifoldHarness("error-task")
		err = engine.Install("some-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)

		// Induce an error in the dependency...
		mh1.InjectError(c, errors.New("ZAP"))

		// ...and check that each task restarts once.
		mh1.AssertOneStart(c)
		mh2.AssertOneStart(c)
	})
}

func (s *EngineSuite) TestErrorPreservesDependencies(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start two tasks, one dependent on the other.
		mh1 := newManifoldHarness()
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)
		mh2 := newManifoldHarness("some-task")
		err = engine.Install("error-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)

		// Induce an error in the dependent...
		mh2.InjectError(c, errors.New("BLAM"))

		// ...and check that only the dependent restarts.
		mh1.AssertNoStart(c)
		mh2.AssertOneStart(c)
	})
}

func (s *EngineSuite) TestCompletedWorkerNotRestartedOnExit(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start a task.
		mh1 := newManifoldHarness()
		err := engine.Install("stop-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		// Stop it without error, and check it doesn't start again.
		mh1.InjectError(c, nil)
		mh1.AssertNoStart(c)
	})
}

func (s *EngineSuite) TestCompletedWorkerRestartedByDependencyChange(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start a task with a dependency.
		mh1 := newManifoldHarness()
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)
		mh2 := newManifoldHarness("some-task")
		err = engine.Install("stop-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)

		// Complete the dependent task successfully.
		mh2.InjectError(c, nil)
		mh2.AssertNoStart(c)

		// Bounce the dependency, and check the dependent is started again.
		mh1.InjectError(c, errors.New("CLUNK"))
		mh1.AssertOneStart(c)
		mh2.AssertOneStart(c)
	})
}

func (s *EngineSuite) TestRestartRestartsDependents(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start a dependency chain of 3 workers.
		mh1 := newManifoldHarness()
		err := engine.Install("error-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)
		mh2 := newManifoldHarness("error-task")
		err = engine.Install("restart-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)
		mh3 := newManifoldHarness("restart-task")
		err = engine.Install("consequent-restart-task", mh3.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh3.AssertOneStart(c)

		// Once they're all running, induce an error at the top level, which will
		// cause the next level to be killed cleanly....
		mh1.InjectError(c, errors.New("ZAP"))

		// ...but should still cause all 3 workers to bounce.
		mh1.AssertOneStart(c)
		mh2.AssertOneStart(c)
		mh3.AssertOneStart(c)
	})
}

func (s *EngineSuite) TestIsFatal(c *gc.C) {
	fatalErr := errors.New("KABOOM")
	s.fix.isFatal = isFatalIf(fatalErr)
	s.fix.dirty = true
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start two independent workers.
		mh1 := newManifoldHarness()
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)
		mh2 := newManifoldHarness()
		err = engine.Install("other-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)

		// Bounce one worker with Just Some Error; check that worker bounces.
		mh1.InjectError(c, errors.New("splort"))
		mh1.AssertOneStart(c)
		mh2.AssertNoStart(c)

		// Bounce another worker with the fatal error; check the engine exits with
		// the right error.
		mh2.InjectError(c, fatalErr)
		mh1.AssertNoStart(c)
		mh2.AssertNoStart(c)
		err = workertest.CheckKilled(c, engine)
		c.Assert(err, gc.Equals, fatalErr)
	})
}

func (s *EngineSuite) TestConfigFilter(c *gc.C) {
	fatalErr := errors.New("kerrang")
	s.fix.isFatal = isFatalIf(fatalErr)
	reportErr := errors.New("meedly-meedly")
	s.fix.filter = func(err error) error {
		c.Check(err, gc.Equals, fatalErr)
		return reportErr
	}
	s.fix.dirty = true
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start a task.
		mh1 := newManifoldHarness()
		err := engine.Install("stop-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		// Inject the fatal error, and check what comes out.
		mh1.InjectError(c, fatalErr)
		err = workertest.CheckKilled(c, engine)
		c.Assert(err, gc.Equals, reportErr)
	})
}

func (s *EngineSuite) TestErrMissing(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// ErrMissing is implicitly and indirectly tested by the default
		// manifoldHarness.start method throughout this suite, but this
		// test explores its behaviour in pathological cases.

		// Start a simple dependency.
		mh1 := newManifoldHarness()
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		// Start a dependent that always complains ErrMissing.
		mh2 := newManifoldHarness("some-task")
		manifold := mh2.Manifold()
		manifold.Start = func(_ dependency.Context) (worker.Worker, error) {
			mh2.starts <- struct{}{}
			return nil, errors.Trace(dependency.ErrMissing)
		}
		err = engine.Install("unmet-task", manifold)
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)

		// Bounce the dependency; check the dependent bounces once or twice (it will
		// react to both the stop and the start of the dependency, but may be lucky
		// enough to only restart once).
		mh1.InjectError(c, errors.New("kerrang"))
		mh1.AssertOneStart(c)
		startCount := 0
		stable := false
		for !stable {
			select {
			case <-mh2.starts:
				startCount++
			case <-time.After(testing.ShortWait):
				stable = true
			}
		}
		c.Logf("saw %d starts", startCount)
		c.Assert(startCount, jc.GreaterThan, 0)
		c.Assert(startCount, jc.LessThan, 3)

		// Stop the dependency for good; check the dependent is restarted just once.
		mh1.InjectError(c, nil)
		mh1.AssertNoStart(c)
		mh2.AssertOneStart(c)
	})
}

func (s *EngineSuite) TestErrBounce(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start a simple dependency.
		mh1 := newManifoldHarness()
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		// Start its dependent.
		mh2 := newResourceIgnoringManifoldHarness("some-task")
		err = engine.Install("another-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)

		// The parent requests bounce causing both to restart.
		// Note(mjs): the lack of a restart delay is not specifically
		// tested as I can't think of a reliable way to do this.
		// TODO(fwereade): yeah, we need a clock to test this
		// properly...
		mh1.InjectError(c, errors.Trace(dependency.ErrBounce))
		mh1.AssertOneStart(c)
		mh2.AssertStart(c) // Might restart more than once
	})
}

func (s *EngineSuite) TestErrUninstall(c *gc.C) {
	s.fix.run(c, func(engine *dependency.Engine) {

		// Start a simple dependency.
		mh1 := newManifoldHarness()
		err := engine.Install("some-task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		// Start its dependent. Note that in this case we want to record all start
		// attempts, even if there are resource errors.
		mh2 := newResourceIgnoringManifoldHarness("some-task")
		err = engine.Install("another-task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)

		// Uninstall the dependency; it should not be restarted, but its dependent should.
		mh1.InjectError(c, errors.Trace(dependency.ErrUninstall))
		mh1.AssertNoStart(c)
		mh2.AssertOneStart(c)

		// Installing a new some-task manifold restarts the dependent.
		mh3 := newManifoldHarness()
		err = engine.Install("some-task", mh3.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh3.AssertOneStart(c)
		mh2.AssertOneStart(c)
	})
}

func (s *EngineSuite) TestFilterStartError(c *gc.C) {
	s.fix.isFatal = alwaysFatal
	s.fix.dirty = true
	s.fix.run(c, func(engine *dependency.Engine) {

		startErr := errors.New("grr crunch")
		filterErr := errors.New("mew hiss")

		err := engine.Install("task", dependency.Manifold{
			Start: func(_ dependency.Context) (worker.Worker, error) {
				return nil, startErr
			},
			Filter: func(in error) error {
				c.Check(in, gc.Equals, startErr)
				return filterErr
			},
		})
		c.Assert(err, jc.ErrorIsNil)

		err = workertest.CheckKilled(c, engine)
		c.Check(err, gc.Equals, filterErr)
	})
}

func (s *EngineSuite) TestFilterWorkerError(c *gc.C) {
	s.fix.isFatal = alwaysFatal
	s.fix.dirty = true
	s.fix.run(c, func(engine *dependency.Engine) {

		injectErr := errors.New("arg squish")
		filterErr := errors.New("blam dink")

		mh := newManifoldHarness()
		manifold := mh.Manifold()
		manifold.Filter = func(in error) error {
			c.Check(in, gc.Equals, injectErr)
			return filterErr
		}
		err := engine.Install("task", manifold)
		c.Assert(err, jc.ErrorIsNil)
		mh.AssertOneStart(c)

		mh.InjectError(c, injectErr)
		err = workertest.CheckKilled(c, engine)
		c.Check(err, gc.Equals, filterErr)
	})
}

// TestWorstError starts an engine with two manifolds that always error
// with fatal errors. We test that the most important error is the one
// returned by the engine.
//
// This test uses manifolds whose workers ignore kill requests. We want
// this (dangerous!) behaviour so that we don't race over which fatal
// error is seen by the engine first.
func (s *EngineSuite) TestWorstError(c *gc.C) {
	worstErr := errors.New("awful error")
	callCount := 0
	s.fix.worstError = func(err1, err2 error) error {
		callCount++
		return worstErr
	}
	s.fix.isFatal = alwaysFatal
	s.fix.dirty = true
	s.fix.run(c, func(engine *dependency.Engine) {

		mh1 := newErrorIgnoringManifoldHarness()
		err := engine.Install("task", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertOneStart(c)

		mh2 := newErrorIgnoringManifoldHarness()
		err = engine.Install("another task", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)

		mh1.InjectError(c, errors.New("ping"))
		mh2.InjectError(c, errors.New("pong"))

		err = workertest.CheckKilled(c, engine)
		c.Check(errors.Cause(err), gc.Equals, worstErr)
		c.Check(callCount, gc.Equals, 2)
	})
}

func (s *EngineSuite) TestConfigValidate(c *gc.C) {
	tests := []struct {
		breakConfig func(*dependency.EngineConfig)
		err         string
	}{{
		func(config *dependency.EngineConfig) {
			config.IsFatal = nil
		}, "IsFatal not specified",
	}, {
		func(config *dependency.EngineConfig) {
			config.WorstError = nil
		}, "WorstError not specified",
	}, {
		func(config *dependency.EngineConfig) {
			config.ErrorDelay = -time.Second
		}, "ErrorDelay is negative",
	}, {
		func(config *dependency.EngineConfig) {
			config.BounceDelay = -time.Second
		}, "BounceDelay is negative",
	}, {
		func(config *dependency.EngineConfig) {
			config.BackoffFactor = 0.9
		}, "BackoffFactor 0.9 must be >= 1",
	}, {
		func(config *dependency.EngineConfig) {
			config.BackoffResetTime = -time.Minute
		}, "BackoffResetTime is negative",
	}, {
		func(config *dependency.EngineConfig) {
			config.MaxDelay = -time.Second
		}, "MaxDelay is negative",
	}, {
		func(config *dependency.EngineConfig) {
			config.Clock = nil
		}, "missing Clock not valid",
	}, {
		func(config *dependency.EngineConfig) {
			config.Metrics = nil
		}, "missing Metrics not valid",
	}, {
		func(config *dependency.EngineConfig) {
			config.Logger = nil
		}, "missing Logger not valid",
	}}

	for i, test := range tests {
		c.Logf("test %d", i)
		config := dependency.EngineConfig{
			IsFatal:     alwaysFatal,
			WorstError:  firstError,
			ErrorDelay:  time.Second,
			BounceDelay: time.Second,
			Clock:       clock.WallClock,
			Metrics:     dependency.DefaultMetrics(),
			Logger:      loggo.GetLogger("test"),
		}
		test.breakConfig(&config)

		c.Logf("config validation...")
		validateErr := config.Validate()
		c.Check(validateErr, gc.ErrorMatches, test.err)

		c.Logf("engine creation...")
		engine, createErr := dependency.NewEngine(config)
		c.Check(engine, gc.IsNil)
		c.Check(createErr, gc.ErrorMatches, "invalid config: "+test.err)
	}
}

func (s *EngineSuite) TestValidateEmptyManifolds(c *gc.C) {
	err := dependency.Validate(dependency.Manifolds{})
	c.Check(err, jc.ErrorIsNil)
}

func (s *EngineSuite) TestValidateTrivialCycle(c *gc.C) {
	err := dependency.Validate(dependency.Manifolds{
		"a": dependency.Manifold{Inputs: []string{"a"}},
	})
	c.Check(err.Error(), gc.Equals, `cycle detected at "a" (considering: map[a:true])`)
}

func (s *EngineSuite) TestValidateComplexManifolds(c *gc.C) {

	// Create a bunch of manifolds with tangled but acyclic dependencies; check
	// that they pass validation.
	manifolds := dependency.Manifolds{
		"root1": dependency.Manifold{},
		"root2": dependency.Manifold{},
		"mid1":  dependency.Manifold{Inputs: []string{"root1"}},
		"mid2":  dependency.Manifold{Inputs: []string{"root1", "root2"}},
		"leaf1": dependency.Manifold{Inputs: []string{"root2", "mid1"}},
		"leaf2": dependency.Manifold{Inputs: []string{"root1", "mid2"}},
		"leaf3": dependency.Manifold{Inputs: []string{"root1", "root2", "mid1", "mid2"}},
	}
	err := dependency.Validate(manifolds)
	c.Check(err, jc.ErrorIsNil)

	// Introduce a cycle; check the manifolds no longer validate.
	manifolds["root1"] = dependency.Manifold{Inputs: []string{"leaf1"}}
	err = dependency.Validate(manifolds)
	c.Check(err, gc.ErrorMatches, "cycle detected at .*")
}

func (s *EngineSuite) TestBackoffFactor(c *gc.C) {
	clock := testclock.NewClock(time.Now())
	config := s.fix.defaultEngineConfig(clock)
	config.ErrorDelay = time.Second
	config.BackoffFactor = 2.0
	config.BackoffResetTime = time.Minute
	config.MaxDelay = 3 * time.Second
	s.fix.config = &config

	s.fix.run(c, func(engine *dependency.Engine) {

		mh := newManifoldHarness()
		mh.startError = errors.New("boom")
		err := engine.Install("task", mh.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		// We should get the task start called, but it returns an error.
		mh.AssertStartAttempt(c)

		// Advance further than 1.1 * ErrorDelay to account for max fuzz.
		c.Assert(clock.WaitAdvance(1200*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertStartAttempt(c)

		// Advance the clock to before 0.9 * 2 * ErrorDelay to ensure
		// that we don't have another start attempt.
		c.Assert(clock.WaitAdvance(1700*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertNoStartAttempt(c)

		// Advance further to 1.1 * 2 * ErrorDelay from the previous failed
		// start, to account for max fuzz.
		c.Assert(clock.WaitAdvance(600*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertStartAttempt(c)

		// Finally hit MaxDelay, so ensure we don't start before 0.9 * MaxDelay.
		// But do start after 1.1 * MaxDelay.
		c.Assert(clock.WaitAdvance(2600*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertNoStartAttempt(c)
		c.Assert(clock.WaitAdvance(800*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertStartAttempt(c)
	})
}

func (s *EngineSuite) TestBackoffFactorOnError(c *gc.C) {
	clock := testclock.NewClock(time.Now())
	config := s.fix.defaultEngineConfig(clock)
	config.ErrorDelay = time.Second
	config.BackoffFactor = 2.0
	config.BackoffResetTime = time.Minute
	config.MaxDelay = 3 * time.Second
	s.fix.config = &config

	s.fix.run(c, func(engine *dependency.Engine) {

		mh := newManifoldHarness()
		err := engine.Install("task", mh.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		// We should get the task start called.
		mh.AssertStartAttempt(c)
		// Inject an immedate error after start
		mh.InjectError(c, errors.Errorf("initial boom"))

		// Advance further than 1.1 * ErrorDelay to account for max fuzz.
		c.Assert(clock.WaitAdvance(1200*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertStartAttempt(c)
		// Wait a bit, but less than BackoffResetTime, and trigger another
		// error. It is ok that nothing is Waiting, because it is only after the
		// error happens that the clock is consulted
		clock.Advance(1000 * time.Millisecond)
		mh.InjectError(c, errors.Errorf("later boom"))

		// Advance the clock to before 0.9 * 2 * ErrorDelay to ensure
		// that we don't have another start attempt.
		c.Assert(clock.WaitAdvance(1700*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertNoStartAttempt(c)

		// Advance further to 1.1 * 2 * ErrorDelay from the previous failed
		// start, to account for max fuzz.
		c.Assert(clock.WaitAdvance(600*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertStartAttempt(c)
		// Again wait a bit, to be clear that the time we wait before
		// starting is based on when it died, not when we last started it.
		clock.Advance(5000 * time.Millisecond)
		mh.InjectError(c, errors.Errorf("last boom"))

		// Finally hit MaxDelay, so ensure we don't start before 0.9 * MaxDelay.
		// But do start after 1.1 * MaxDelay.
		c.Assert(clock.WaitAdvance(2600*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertNoStartAttempt(c)
		c.Assert(clock.WaitAdvance(800*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertStartAttempt(c)

		// We need to advance the clock after we have recorded startTime, which
		// means we need to wait for the engine to notice the started event,
		// process it, and be ready to process the next event. Installing another
		// manifold is done in the same loop.
		err = engine.Install("task2", mh.Manifold())
		c.Assert(err, jc.ErrorIsNil)

		// Now advance longer than the BackoffResetTime, indicating the
		// worker was running successfully for "long enough" before we
		// trigger a failure
		clock.Advance(2 * time.Minute)
		mh.InjectError(c, errors.Errorf("after successful run"))
		// Ensure we try to start after a short fuzzed delay
		c.Assert(clock.WaitAdvance(1200*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		mh.AssertStartAttempt(c)
	})
}

func (s *EngineSuite) TestBackoffFactorOverflow(c *gc.C) {
	clock := testclock.NewClock(time.Now())
	config := s.fix.defaultEngineConfig(clock)
	config.ErrorDelay = time.Second
	config.BackoffFactor = 100.0
	config.BackoffResetTime = time.Minute
	config.MaxDelay = time.Minute
	s.fix.config = &config

	s.fix.run(c, func(engine *dependency.Engine) {

		// What we are testing here is that the first error delay is
		// approximately one second, then ten seconds, then it maxes
		// out at one minute

		mh := newManifoldHarness()
		mh.startError = errors.New("boom")
		err := engine.Install("task", mh.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		// We should get the task start called, but it returns an error.
		c.Logf("install start attempt")
		mh.AssertStartAttempt(c)

		// Advance further than 1.1 * ErrorDelay to account for max fuzz.
		c.Assert(clock.WaitAdvance(1200*time.Millisecond, testing.ShortWait, 1), jc.ErrorIsNil)
		c.Logf("first failure start attempt")
		mh.AssertStartAttempt(c)

		// Now we are at the max of one minute delay.
		// Here we are now testing nested math.
		// The time.Duration of the full calculation wraps and becomes negative
		// after 6 failures.
		// The total time becomes +Inf after 151 iterations
		// The pow calculation becomes +Inf after 156 iterations.
		// So to be safe, lets iterate a couple of hundred times.
		for i := 3; i < 200; i++ {
			c.Assert(clock.WaitAdvance(70*time.Second, testing.ShortWait, 1), jc.ErrorIsNil)
			c.Logf("%d failure start attempt", i)
			mh.AssertStartAttempt(c)
		}
	})
}

func (s *EngineSuite) TestRestartDependentWhenAborted(c *gc.C) {
	clock := testclock.NewClock(time.Now())
	config := s.fix.defaultEngineConfig(clock)
	config.BounceDelay = time.Second
	config.BackoffFactor = 2.0
	s.fix.config = &config

	s.fix.run(c, func(engine *dependency.Engine) {

		// Start a worker that has a dependency that isn't installed.
		mh1 := newManifoldHarness("task2", "task3")
		err := engine.Install("task1", mh1.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh1.AssertNoStart(c)

		// Right now task1 is waiting for a dependency change.

		// Start task2 dependency, this will trigger a restart of task1
		// in BounceDelay.
		mh2 := newResourceIgnoringManifoldHarness()
		err = engine.Install("task2", mh2.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh2.AssertOneStart(c)

		c.Logf("advance 500ms")
		clock.WaitAdvance(500*time.Millisecond, testing.ShortWait, 1)
		mh1.AssertNoStart(c)

		// Not start task3, this will interrupt the start of task1.
		mh3 := newResourceIgnoringManifoldHarness()
		err = engine.Install("task3", mh3.Manifold())
		c.Assert(err, jc.ErrorIsNil)
		mh3.AssertOneStart(c)

		// task1 should end up started after BounceDelay + fuzz
		c.Logf("advance 1200ms")
		// NOTE: we have two waiters because the first loop that was aborted
		// is still technically in the test clock waiting.
		clock.WaitAdvance(1200*time.Millisecond, testing.ShortWait, 2)

		mh1.AssertOneStart(c)
	})
}
