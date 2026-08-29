package lockfile

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The lock has to survive being probed.
//
// Both watchdog processes call Held every five seconds, and Held takes the lock
// to answer. While Release still unlinked the file, a probe that landed between
// another process's open and its flock left the two of them locked to different
// inodes — both certain they were the only one. This is that collision, run
// hard: a probe loop against a holder that keeps taking and dropping the lock.
func TestProbingDoesNotProduceTwoHolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")

	var holders atomic.Int32
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Two contenders, each repeatedly acquiring and releasing.
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				lock, err := Acquire(path)
				if err != nil {
					continue // held by the other one, which is correct
				}
				if n := holders.Add(1); n != 1 {
					t.Errorf("%d processes hold the lock at once", n)
				}
				time.Sleep(time.Millisecond)
				holders.Add(-1)
				lock.Release()
			}
		}()
	}

	// And a watchdog probing throughout, which is what used to break it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			Held(path)
		}
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// A lock genuinely held reads as held, however often it is asked.
func TestHeldIsTrueWhileSomebodyHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()

	for range 20 {
		if !Held(path) {
			t.Fatal("Held said the lock was free while this process held it")
		}
	}
}

// A lock in a directory that does not exist yet is not held by anyone.
//
// Held answers "yes" when it cannot open the lock at all, because a lock it
// cannot see is not one it may declare free. That rule made the first probe on
// a fresh state directory report a running daemon and stopped `daemon --detach`
// from ever starting one — silently, with exit 0.
func TestHeldCreatesTheDirectoryItProbesIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "daemon.lock")
	if Held(path) {
		t.Fatal("an unused lock in a fresh directory reported as held")
	}
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after probe: %v", err)
	}
	defer lock.Release()
	if !Held(path) {
		t.Error("Held said free while this process held the lock")
	}
}
