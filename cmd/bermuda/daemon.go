package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bon5co/bermuda/v2/internal/herdrcli"
	"github.com/bon5co/bermuda/v2/internal/lockfile"
	"github.com/bon5co/bermuda/v2/internal/runner"
	"github.com/bon5co/bermuda/v2/internal/sched"
	"github.com/bon5co/bermuda/v2/internal/store"
)

// daemonOpts is what `bermuda daemon` accepts.
type daemonOpts struct {
	tick        *time.Duration
	concurrency *int
	detach      *bool
}

// daemonFlagSet is separate from daemonCmd so a test can ask which flags exist
// without running a scheduler — which is how the plugin manifest came to invoke
// a --detach that nothing defined.
func daemonFlagSet() (*flag.FlagSet, *daemonOpts) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	return fs, &daemonOpts{
		tick:        fs.Duration("tick", defaultTick, "how often to check for due jobs"),
		concurrency: fs.Int("concurrency", defaultConcurrency, "maximum jobs running at once"),
		detach:      fs.Bool("detach", false, "start the scheduler in the background and return"),
	}
}

// daemonCmd runs the scheduler loop.
//
// The daemon is hosted by the Herdr plugin's startup hook, which fires again
// on live handoff, so it must be safe to start repeatedly: the lock makes
// every start after the first a no-op.
func daemonCmd(argv []string) error {
	fs, opts := daemonFlagSet()
	if err := fs.Parse(argv); err != nil {
		return err
	}
	tick, concurrency, detach := opts.tick, opts.concurrency, opts.detach

	// --detach is what a startup hook needs: the hook is reaped rather than
	// supervised, so a foreground daemon dies with it. The plugin manifest and
	// the README have both asked for this flag since before it existed, which
	// meant one failed hook printing "flag provided but not defined" on every
	// Herdr start.
	if *detach {
		return ensureRunning("--tick", tick.String())
	}

	lock, err := lockfile.Acquire(lockPath(roleDaemon))
	if err != nil {
		var held *lockfile.ErrHeld
		if errors.As(err, &held) {
			// Not an error: repeated starts are how the startup hook, the
			// board, and the sentinel all behave.
			fmt.Println("bermuda:", held.Error())
			return nil
		}
		return err
	}
	defer lock.Release()

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	// Whatever killed the previous daemon may have stranded a run mid-flight,
	// and the sentinel restarts within seconds — so this is the first chance to
	// put those rows right.
	reconcileOnStart(s)

	ctx, stop := signalContext()
	defer stop()

	// The scheduler watches the sentinel just as the sentinel watches it, so
	// killing either one leaves a survivor that restores the pair.
	go watchPeer(ctx, roleDaemon, stop)

	d := &daemon{store: s, tick: *tick, slots: make(chan struct{}, *concurrency)}
	fmt.Printf("bermuda: daemon started (tick %s, concurrency %d, pid %d)\n",
		*tick, *concurrency, os.Getpid())
	d.run(ctx)
	fmt.Println("bermuda: daemon stopped")
	return nil
}

const (
	defaultTick        = 5 * time.Second
	defaultConcurrency = 4
	// defaultReconcileEvery is how often the daemon re-reads parked runs
	// against the disk. It is minutes rather than seconds because a park it
	// corrects is one an agent finished after the supervisor gave up, and an
	// agent that overran by that much is not going to finish within a tick.
	defaultReconcileEvery = 5 * time.Minute
)

// signalContext cancels on interrupt or termination.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// daemon evaluates schedules and launches due runs.
type daemon struct {
	store *store.Store
	tick  time.Duration
	// slots bounds how many jobs run at once.
	slots chan struct{}

	mu sync.Mutex
	// inflight is the set of job ids currently running, which is what enforces
	// the no-overlap rule.
	inflight map[string]bool
	wg       sync.WaitGroup

	// exec runs one job. It is a field so a test can count what a sweep
	// launches without starting agents — the whole execution path was
	// untestable, and a catchup policy that launched one run per sweep instead
	// of the backlog it owed went unnoticed for that reason.
	exec func(context.Context, *store.Store, store.Job, string) (*runner.Run, error)

	// reconcileEvery is how often parked runs are re-read from disk. Zero means
	// the default; a test sets it short.
	reconcileEvery time.Duration
	// reconcile corrects parks the disk contradicts. A field for the same
	// reason exec is one.
	reconcile func(context.Context, *store.Store) (int, error)
}

// reconcileStale corrects the runs that were parked for want of a result which
// has since been written.
//
// A park is a verdict about a moment, and the agent outlives the process that
// passed it: a run whose agent wrote result.json ten minutes after the deadline
// stays filed as broken until someone restarts the daemon or runs the ensure
// hook by hand. Nothing else on this machine looks, so the self-heal shift
// spends an agent every morning re-deciding that a finished job is fine.
func (d *daemon) reconcileStale(ctx context.Context) {
	fn := d.reconcile
	if fn == nil {
		fn = reconcileParked
	}
	n, err := fn(ctx, d.store)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: reconcile parked:", err)
		return
	}
	if n > 0 {
		fmt.Printf("bermuda: corrected %d park(s) the disk contradicts\n", n)
	}
}

// execute runs one job, through whatever exec is set to.
func (d *daemon) execute(ctx context.Context, j store.Job) (*runner.Run, error) {
	if d.exec != nil {
		return d.exec(ctx, d.store, j, "scheduled")
	}
	return Execute(ctx, d.store, j, "scheduled")
}

func (d *daemon) run(ctx context.Context) {
	d.inflight = map[string]bool{}
	t := time.NewTicker(d.tick)
	defer t.Stop()
	every := d.reconcileEvery
	if every == 0 {
		every = defaultReconcileEvery
	}
	rt := time.NewTicker(every)
	defer rt.Stop()
	for {
		select {
		case <-ctx.Done():
			// Let running jobs finish: killing an agent mid-turn would leave a
			// run that is neither done nor parked.
			d.wg.Wait()
			return
		case <-t.C:
			d.sweep(ctx)
		case <-rt.C:
			d.reconcileStale(ctx)
		}
	}
}

// sweep launches every job that is due and not already running.
func (d *daemon) sweep(ctx context.Context) {
	d.retireClosedWorkspaces(ctx)
	jobs, err := d.store.Jobs(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: read jobs:", err)
		return
	}
	now := time.Now()
	for _, j := range jobs {
		var anchor time.Time
		if last, err := d.store.LastRun(ctx, j.ID); err == nil {
			anchor = last.StartedAt
		}
		fires, err := sched.Due(j, anchor, now)
		if err != nil {
			// A single malformed schedule must not stop the sweep.
			fmt.Fprintln(os.Stderr, "bermuda:", err)
			continue
		}
		if len(fires) == 0 {
			continue
		}
		if !d.claim(j.ID) {
			// Still running from a previous fire. Skipping is the safe
			// default: a job whose runs outlast its interval would otherwise
			// stack up agents without bound.
			continue
		}
		// One claim for the whole backlog, and the runs go in series inside
		// it. This loop used to call claim per fire and break as soon as one
		// was refused, so catchup=all launched exactly one run per sweep — and
		// the next sweep measured from that run, which put the rest of the
		// backlog behind the anchor and lost it. Three documented policies,
		// one behaviour.
		if len(fires) > 1 {
			fmt.Printf("bermuda: job %s owes %d fires; replaying them in series\n",
				j.ID, len(fires))
		}
		d.launch(ctx, j, len(fires))
	}
}

// retireClosedWorkspaces closes the thread of any workspace herdr no longer
// reports.
//
// A workspace thread is created without anyone asking, so it has to be tidied
// without anyone asking too: otherwise `thread list` fills with the spaces of
// every window ever opened, and choosing where to write means reading a list of
// conversations that ended.
//
// Closing is not deleting. The messages stay readable — what changed on this
// machine is still true after the window is shut — the thread just leaves the
// list and stops accepting writes.
//
// Every failure here is silent except an unreachable herdr, and even that only
// skips the sweep. Herdr reporting no workspaces because it is down must never
// be read as every workspace having closed: that would retire every thread on
// the machine at once, and closing is not something a tick should be able to do
// on bad information.
func (d *daemon) retireClosedWorkspaces(ctx context.Context) {
	c := herdrcli.New()
	if c == nil {
		return
	}
	spaces, err := c.WorkspaceList(ctx)
	if err != nil || len(spaces) == 0 {
		return
	}
	live := make([]string, 0, len(spaces))
	for _, w := range spaces {
		live = append(live, w.WorkspaceID)
	}
	closed, err := d.store.CloseVanishedWorkspaces(ctx, live, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "bermuda: retire closed workspaces:", err)
		return
	}
	for _, id := range closed {
		fmt.Printf("bermuda: workspace gone, thread %s closed — `bermuda thread log "+
			"--thread %s` still reads it\n", id, id)
	}
}

// claim marks a job as running, reporting false when it already is.
func (d *daemon) claim(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inflight[id] {
		return false
	}
	d.inflight[id] = true
	return true
}

func (d *daemon) release(id string) {
	d.mu.Lock()
	delete(d.inflight, id)
	d.mu.Unlock()
}

func (d *daemon) launch(ctx context.Context, j store.Job, times int) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer d.release(j.ID)

		for i := range times {
			// Block until a slot frees. The job is already claimed, so it
			// cannot be launched twice while it waits here.
			select {
			case d.slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			// A backlog stops at shutdown; a run already started does not.
			if i > 0 && ctx.Err() != nil {
				<-d.slots
				return
			}

			// The run gets a context that shutdown does not cancel. The
			// scheduler's ctx reaches exec.CommandContext through herdrcli, so
			// SIGTERM used to kill every agent mid-turn — leaving exactly the
			// run that is neither done nor parked that d.run() claims to
			// prevent by waiting. The run's own timeout still bounds it.
			run, err := d.execute(context.WithoutCancel(ctx), j)
			<-d.slots
			switch {
			case err != nil:
				fmt.Fprintf(os.Stderr, "bermuda: job %s: %v\n", j.ID, err)
			case run != nil:
				fmt.Printf("bermuda: job %s %s (%s)\n", j.ID, run.Outcome, run.RunID)
			}
		}
	}()
}
