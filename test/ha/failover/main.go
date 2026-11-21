// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// failover measures what the Mixer HA design relies on: how long a
// replicated JetStream key-value bucket refuses writes when its Raft leader
// dies, and whether a write it acknowledged survives.
//
// It starts three NATS servers with JetStream as separate processes on this
// machine, clustered over routes, and a bucket with three replicas, shaped
// like the registry bucket. A writer makes revision-checked updates, as the
// registry does. Each round kills one server with SIGKILL, measures the gap
// between the kill and the next acknowledged write, checks that no
// acknowledged value was lost, then restarts the server and waits until every
// replica is current again.
//
//	go run ./test/ha/failover -rounds 20 -target leader
//	go run ./test/ha/failover -rounds 10 -target follower
//
// It measures the mechanism on one machine: no network between the nodes,
// so the number is the same-site floor, not a cross-site figure.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const bucket = "failover_bench"

// Probe settings, taken from the run's config. A probe asks the cluster
// for its stream leader; at WAN latency a fixed sub-second timeout expires
// before the answer arrives and reads a leader as missing.
var (
	probeTimeout = 2 * time.Second
	probeLimit   = 60 * time.Second
)

// config holds every knob of a run. Nothing is fixed in the code: each
// value has a flag with a documented default.
type config struct {
	rounds         int
	target         string
	interval       time.Duration
	opTimeout      time.Duration
	settle         time.Duration
	dir            string
	formTimeout    time.Duration
	recoverTimeout time.Duration
	faultDuration  time.Duration
	// External cluster mode: the servers already run (the Docker testbed),
	// and faults are shell commands with {node} replaced by the victim.
	clusterURLs string
	nodeNames   string
	faultCmd    string
	healCmd     string
	label       string
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "server" {
		runServer(os.Args[2:])
		return
	}
	var c config
	flag.IntVar(&c.rounds, "rounds", 20, "fault rounds")
	flag.StringVar(&c.target, "target", "leader", "which server the fault hits: leader or follower")
	flag.DurationVar(&c.interval, "write-interval", 5*time.Millisecond, "pause between writes")
	flag.DurationVar(&c.opTimeout, "op-timeout", 2*time.Second, "timeout of one write and of a cluster probe; it must exceed a replicated write's round trip, or a healthy WAN cluster reads as down")
	flag.DurationVar(&c.settle, "settle", 3*time.Second, "pause between rounds once the cluster is whole")
	flag.StringVar(&c.dir, "dir", "", "working directory for local servers (default: a temporary one)")
	flag.DurationVar(&c.formTimeout, "form-timeout", 60*time.Second, "how long the cluster may take to form or become whole")
	flag.DurationVar(&c.recoverTimeout, "recover-timeout", 60*time.Second, "how long after a fault (after its duration) a write may take to be acknowledged")
	flag.DurationVar(&c.faultDuration, "fault-duration", 0, "how long a fault lasts before the heal command runs (0: heal once writes resume, as for a kill)")
	flag.StringVar(&c.clusterURLs, "cluster", "", "comma-separated client URLs of an external cluster (empty: start local servers)")
	flag.StringVar(&c.nodeNames, "nodes", "n1,n2,n3", "server names of the external cluster, as the servers report them")
	flag.StringVar(&c.faultCmd, "fault-cmd", "", "external mode: command that applies the fault to {node}")
	flag.StringVar(&c.healCmd, "heal-cmd", "", "external mode: command that heals {node}")
	flag.StringVar(&c.label, "label", "kill", "name of the fault, for the report")
	flag.Parse()
	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, "failover:", err)
		os.Exit(1)
	}
}

// runServer is one cluster member, started as a child process.
func runServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	name := fs.String("name", "", "server name")
	port := fs.Int("port", 0, "client port")
	cport := fs.Int("cluster-port", 0, "route port")
	routes := fs.String("routes", "", "comma-separated route URLs")
	store := fs.String("store", "", "JetStream store dir")
	host := fs.String("host", "127.0.0.1", "address the client and route listeners bind to")
	_ = fs.Parse(args)

	opts := &natsserver.Options{
		ServerName: *name, Host: *host, Port: *port,
		JetStream: true, StoreDir: *store, NoSigs: false,
		Cluster: natsserver.ClusterOpts{Name: "ha-bench", Host: *host, Port: *cport},
	}
	for _, r := range strings.Split(*routes, ",") {
		if u, err := url.Parse(r); err == nil {
			opts.Routes = append(opts.Routes, u)
		}
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	srv.Start()
	srv.WaitForShutdown()
}

type node struct {
	name        string
	port, cport int
	store       string
	cmd         *exec.Cmd
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func (n *node) start(routes string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	n.cmd = exec.Command(self, "server", "-name", n.name, "-port", fmt.Sprint(n.port), //nolint:gosec // own binary, own flags
		"-cluster-port", fmt.Sprint(n.cport), "-routes", routes, "-store", n.store)
	n.cmd.Stdout, n.cmd.Stderr = nil, nil
	return n.cmd.Start()
}

func (n *node) kill() {
	if n.cmd != nil && n.cmd.Process != nil {
		_ = n.cmd.Process.Signal(syscall.SIGKILL)
		_, _ = n.cmd.Process.Wait()
	}
}

// cluster applies and heals a fault on one named server.
type cluster interface {
	names() []string
	fault(name string) error
	heal(name string) error
	close()
}

// localCluster runs three servers as child processes; the fault is SIGKILL
// and the heal a restart.
type localCluster struct {
	nodes  []*node
	byName map[string]*node
	routes string
}

func (l *localCluster) names() []string {
	var out []string
	for _, n := range l.nodes {
		out = append(out, n.name)
	}
	return out
}
func (l *localCluster) fault(name string) error { l.byName[name].kill(); return nil }
func (l *localCluster) heal(name string) error  { return l.byName[name].start(l.routes) }
func (l *localCluster) close() {
	for _, n := range l.nodes {
		n.kill()
	}
}

// externalCluster runs the fault and heal commands, with {node} replaced.
type externalCluster struct {
	nodeNames []string
	faultCmd  string
	healCmd   string
}

func (e *externalCluster) names() []string         { return e.nodeNames }
func (e *externalCluster) fault(name string) error { return sh(e.faultCmd, name) }
func (e *externalCluster) heal(name string) error  { return sh(e.healCmd, name) }
func (e *externalCluster) close()                  {}

func sh(tmpl, name string) error {
	cmd := exec.Command("sh", "-c", strings.ReplaceAll(tmpl, "{node}", name)) //nolint:gosec // operator-supplied test command
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%q: %w: %s", cmd.Args[2], err, out)
	}
	return nil
}

func startLocal(dir string) (*localCluster, []string, error) {
	l := &localCluster{byName: map[string]*node{}}
	var routeURLs, clientURLs []string
	for i := 0; i < 3; i++ {
		n := &node{name: fmt.Sprintf("n%d", i+1), port: freePort(), cport: freePort(), store: filepath.Join(dir, fmt.Sprintf("n%d", i+1))}
		l.nodes = append(l.nodes, n)
		l.byName[n.name] = n
		routeURLs = append(routeURLs, fmt.Sprintf("nats://127.0.0.1:%d", n.cport))
		clientURLs = append(clientURLs, fmt.Sprintf("nats://127.0.0.1:%d", n.port))
	}
	l.routes = strings.Join(routeURLs, ",")
	for _, n := range l.nodes {
		if err := n.start(l.routes); err != nil {
			l.close()
			return nil, nil, err
		}
	}
	return l, clientURLs, nil
}

func run(c config) error {
	probeTimeout, probeLimit = c.opTimeout, c.formTimeout
	if c.target != "leader" && c.target != "follower" {
		return fmt.Errorf("-target must be leader or follower")
	}
	var cl cluster
	var clientURLs []string
	if c.clusterURLs == "" {
		dir := c.dir
		if dir == "" {
			d, err := os.MkdirTemp("", "ha-failover-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(d) }()
			dir = d
		}
		l, urls, err := startLocal(dir)
		if err != nil {
			return err
		}
		cl, clientURLs = l, urls
	} else {
		if c.faultCmd == "" || c.healCmd == "" {
			return fmt.Errorf("-cluster needs -fault-cmd and -heal-cmd")
		}
		cl = &externalCluster{nodeNames: strings.Split(c.nodeNames, ","), faultCmd: c.faultCmd, healCmd: c.healCmd}
		clientURLs = strings.Split(c.clusterURLs, ",")
	}
	defer cl.close()

	nc, err := nats.Connect(strings.Join(clientURLs, ","), nats.MaxReconnects(-1), nats.ReconnectWait(50*time.Millisecond), nats.RetryOnFailedConnect(true))
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var kv jetstream.KeyValue
	deadline := time.Now().Add(c.formTimeout)
	for {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		kv, err = js.CreateOrUpdateKeyValue(cctx, jetstream.KeyValueConfig{Bucket: bucket, Replicas: 3, History: 1, Storage: jetstream.FileStorage})
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cluster did not form: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := waitWhole(ctx, js, c.formTimeout); err != nil {
		return err
	}

	w := &writer{kv: kv, interval: c.interval, opTimeout: c.opTimeout}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); w.loop(stop) }()
	defer func() { close(stop); wg.Wait() }()
	time.Sleep(c.settle)

	where := "3 nats-server processes on one host (loopback routes)"
	if c.clusterURLs != "" {
		where = "external cluster " + c.clusterURLs
	}
	fmt.Printf("machine: %s; %s\n", machine(), where)
	fmt.Printf("fault=%s target=%s rounds=%d fault-duration=%s write-interval=%s op-timeout=%s\n\n",
		c.label, c.target, c.rounds, c.faultDuration, c.interval, c.opTimeout)
	var gaps, elections []time.Duration
	lost, noRecovery := 0, 0
	for r := 1; r <= c.rounds; r++ {
		leader, err := streamLeader(ctx, js)
		if err != nil {
			return err
		}
		victim := leader
		if c.target == "follower" {
			for _, n := range cl.names() {
				if n != leader {
					victim = n
					break
				}
			}
		}
		staleBefore := w.stale.Load()
		clientOnVictim := nc.ConnectedServerName() == victim
		if err := cl.fault(victim); err != nil {
			return fmt.Errorf("round %d: fault: %w", r, err)
		}
		// The clock starts once the fault command has returned: the fault is
		// in effect from here, not from when the command was started.
		faultAt := time.Now()
		electedAfter := make(chan time.Duration, 1)
		go func() {
			electedAfter <- waitNewLeader(ctx, js, leader, faultAt, c.target, c.faultDuration+c.recoverTimeout)
		}()

		var gap time.Duration
		var ok bool
		if c.faultDuration > 0 {
			// A held fault (a partition): writes may resume while it lasts,
			// or only once it heals. Either way the gap counts from the fault.
			healAt := faultAt.Add(c.faultDuration)
			gap, ok = w.nextAckAfter(faultAt, time.Until(healAt))
			time.Sleep(time.Until(healAt))
			if err := cl.heal(victim); err != nil {
				return fmt.Errorf("round %d: heal: %w", r, err)
			}
			if !ok {
				gap, ok = w.nextAckAfter(faultAt, c.recoverTimeout)
			}
		} else {
			gap, ok = w.nextAckAfter(faultAt, c.recoverTimeout)
		}
		elected := <-electedAfter
		if !ok {
			// No recovery within the timeout is a result, not an error: record
			// it, heal, and go on to the next round.
			noRecovery++
			fmt.Printf("round %2d: %s on %s (leader was %s): NO WRITE ACKNOWLEDGED within %s of the fault\n",
				r, c.label, victim, leader, c.faultDuration+c.recoverTimeout)
			if c.faultDuration == 0 {
				if err := cl.heal(victim); err != nil {
					return fmt.Errorf("round %d: heal: %w", r, err)
				}
			}
			if err := waitWhole(ctx, js, c.formTimeout); err != nil {
				return fmt.Errorf("round %d: %w", r, err)
			}
			time.Sleep(c.settle)
			continue
		}
		gaps = append(gaps, gap)
		elections = append(elections, elected)
		newLeader, _ := streamLeader(ctx, js)
		during := ""
		if c.faultDuration > 0 {
			during = fmt.Sprintf(" (during the fault: %t)", gap < c.faultDuration)
		}
		if c.faultDuration == 0 {
			if err := cl.heal(victim); err != nil {
				return fmt.Errorf("round %d: heal: %w", r, err)
			}
		}
		if err := waitWhole(ctx, js, c.formTimeout); err != nil {
			return fmt.Errorf("round %d: %w", r, err)
		}
		// Durability: every value acknowledged so far must be on the leader.
		ackedSoFar := w.maxAcked()
		// Right after a heal the leader may still answer "temporarily
		// unavailable"; ask again until it answers or the timeout passes.
		var onLeader uint64
		var errLeader error
		for until := time.Now().Add(c.formTimeout); ; {
			lctx, lcancel := context.WithTimeout(ctx, 2*time.Second)
			onLeader, errLeader = leaderValue(lctx, nc)
			lcancel()
			if errLeader == nil || time.Now().After(until) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		lostNote := "ok"
		if errLeader != nil {
			lostNote = "unverified: " + errLeader.Error()
		} else if onLeader < ackedSoFar {
			lost++
			lostNote = fmt.Sprintf("LOST: leader has %d, %d was acknowledged", onLeader, ackedSoFar)
		}
		fmt.Printf("round %2d: %s on %s (leader was %s, client on it: %-5t) leader known after %6.1f ms, writes back after %6.1f ms%s, new leader %s, stale reads=%d, durability %s\n",
			r, c.label, victim, leader, clientOnVictim, ms(elected), ms(gap), during, newLeader, w.stale.Load()-staleBefore, lostNote)
		time.Sleep(c.settle)
	}

	fmt.Printf("\nrounds without recovery within %s: %d of %d\n", c.faultDuration+c.recoverTimeout, noRecovery, c.rounds)
	if len(gaps) == 0 {
		fmt.Printf("write unavailability, %s on the %s: no round recovered\n", c.label, c.target)
	} else {
		fmt.Printf("write unavailability, %s on the %s (%d recovered rounds):\n  %s\n", c.label, c.target, len(gaps), stats(gaps))
		fmt.Printf("time until a leader is reported (%d rounds):\n  %s\n", len(elections), stats(elections))
	}
	fmt.Printf("acknowledged writes: %d, rounds with an acknowledged write missing on the leader: %d, stale reads: %d\n",
		w.acked.Load(), lost, w.stale.Load())
	return nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func stats(ds []time.Duration) string {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	pct := func(p float64) time.Duration { return s[int(p*float64(len(s)-1))] }
	return fmt.Sprintf("min %.1f ms  avg %.1f ms  p50 %.1f ms  p95 %.1f ms  max %.1f ms",
		ms(s[0]), ms(sum/time.Duration(len(s))), ms(pct(0.5)), ms(pct(0.95)), ms(s[len(s)-1]))
}

// ackRecord is one acknowledged write: when it was issued and when the
// acknowledgement came back.
type ackRecord struct{ issued, acked time.Time }

// writer makes revision-checked updates of one key with an increasing
// counter. It records every acknowledged value and counts a read that shows
// a value lower than one already acknowledged (a stale read).
type writer struct {
	kv        jetstream.KeyValue
	interval  time.Duration
	opTimeout time.Duration

	mu    sync.Mutex
	acks  []ackRecord
	acked atomic.Int64
	// stale counts reads that returned a value older than one already
	// acknowledged. A key-value read may be served by any replica, so this
	// is staleness, not loss: loss is checked on the leader (leaderValue).
	stale  atomic.Int64
	maxAck uint64
}

func (w *writer) loop(stop <-chan struct{}) {
	var rev, next uint64
	for {
		select {
		case <-stop:
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), w.opTimeout)
		if rev == 0 {
			e, err := w.kv.Get(ctx, "entity.4.bench")
			switch {
			case err == nil:
				v := binary.BigEndian.Uint64(e.Value())
				w.checkRead(v)
				rev, next = e.Revision(), v+1
			case errors.Is(err, jetstream.ErrKeyNotFound):
				val := make([]byte, 8)
				binary.BigEndian.PutUint64(val, 1)
				issued := time.Now()
				if r, errC := w.kv.Create(ctx, "entity.4.bench", val); errC == nil {
					w.ack(1, issued)
					rev, next = r, 2
				}
			}
			cancel()
			time.Sleep(w.interval)
			continue
		}
		val := make([]byte, 8)
		binary.BigEndian.PutUint64(val, next)
		issued := time.Now()
		r, err := w.kv.Update(ctx, "entity.4.bench", val, rev)
		cancel()
		if err == nil {
			w.ack(next, issued)
			rev, next = r, next+1
		} else {
			rev = 0 // read again: the update may have landed without its ack
		}
		time.Sleep(w.interval)
	}
}

func (w *writer) ack(v uint64, issued time.Time) {
	w.mu.Lock()
	w.acks = append(w.acks, ackRecord{issued: issued, acked: time.Now()})
	if v > w.maxAck {
		w.maxAck = v
	}
	w.mu.Unlock()
	w.acked.Add(1)
}

func (w *writer) maxAcked() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxAck
}

func (w *writer) checkRead(v uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if v < w.maxAck {
		w.stale.Add(1)
	}
}

// nextAckAfter waits for the first acknowledged write issued after t and
// returns how long after t its acknowledgement came. A write issued before t
// and acknowledged after it says nothing about the cluster after the fault,
// so it does not count.
func (w *writer) nextAckAfter(t time.Time, limit time.Duration) (time.Duration, bool) {
	end := time.Now().Add(limit)
	for time.Now().Before(end) {
		w.mu.Lock()
		for i := len(w.acks) - 1; i >= 0 && w.acks[i].issued.After(t); i-- {
			if i == 0 || !w.acks[i-1].issued.After(t) {
				d := w.acks[i].acked.Sub(t)
				w.mu.Unlock()
				return d, true
			}
		}
		w.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	return 0, false
}

// leaderValue reads the key's current value through the stream leader's
// message API. A plain key-value read may be served by any replica
// (direct get) and so may be stale; this one cannot.
func leaderValue(ctx context.Context, nc *nats.Conn) (uint64, error) {
	req := []byte(`{"last_by_subj":"$KV.` + bucket + `.entity.4.bench"}`)
	msg, err := nc.RequestWithContext(ctx, "$JS.API.STREAM.MSG.GET.KV_"+bucket, req)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Message struct {
			Data []byte `json:"data"`
		} `json:"message"`
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return 0, err
	}
	if resp.Error != nil {
		return 0, errors.New(resp.Error.Description)
	}
	if len(resp.Message.Data) != 8 {
		return 0, fmt.Errorf("unexpected value of %d bytes", len(resp.Message.Data))
	}
	return binary.BigEndian.Uint64(resp.Message.Data), nil
}

func streamLeader(ctx context.Context, js jetstream.JetStream) (string, error) {
	deadline := time.Now().Add(probeLimit)
	for time.Now().Before(deadline) {
		cctx, cancel := context.WithTimeout(ctx, probeTimeout)
		s, err := js.Stream(cctx, "KV_"+bucket)
		if err == nil {
			var info *jetstream.StreamInfo
			info, err = s.Info(cctx)
			if err == nil && info.Cluster != nil && info.Cluster.Leader != "" {
				cancel()
				return info.Cluster.Leader, nil
			}
		}
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
	return "", errors.New("no stream leader")
}

// waitNewLeader polls until the stream reports a leader other than the
// faulted one (for a follower kill, any leader) and returns how long after
// the kill that was.
func waitNewLeader(ctx context.Context, js jetstream.JetStream, old string, killAt time.Time, target string, limit time.Duration) time.Duration {
	for time.Since(killAt) < limit {
		cctx, cancel := context.WithTimeout(ctx, probeTimeout)
		if s, err := js.Stream(cctx, "KV_"+bucket); err == nil {
			if info, errInfo := s.Info(cctx); errInfo == nil && info.Cluster != nil && info.Cluster.Leader != "" &&
				(target == "follower" || info.Cluster.Leader != old) {
				cancel()
				return time.Since(killAt)
			}
		}
		cancel()
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(killAt)
}

// waitWhole waits until the stream has a leader and both followers are
// current, so the next round starts from a full cluster.
func waitWhole(ctx context.Context, js jetstream.JetStream, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		cctx, cancel := context.WithTimeout(ctx, probeTimeout)
		s, err := js.Stream(cctx, "KV_"+bucket)
		if err == nil {
			info, errInfo := s.Info(cctx)
			if errInfo == nil && info.Cluster != nil && info.Cluster.Leader != "" && len(info.Cluster.Replicas) == 2 {
				current := true
				for _, p := range info.Cluster.Replicas {
					if !p.Current || p.Offline {
						current = false
					}
				}
				if current {
					cancel()
					return nil
				}
			}
		}
		cancel()
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("cluster did not become whole again")
}

func machine() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	model := "unknown CPU"
	if err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				model = strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
				break
			}
		}
	}
	host, _ := os.Hostname()
	return fmt.Sprintf("%s, %s", host, model)
}
