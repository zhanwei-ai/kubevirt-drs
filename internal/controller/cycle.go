// Package controller holds the two runtime pieces: the once-a-minute Cycle and the DRSRecommendation
// reconciler that applies approved recommendations.
package controller

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/executor"
	"github.com/zhanwei-ai/kubevirt-drs/internal/kube"
	"github.com/zhanwei-ai/kubevirt-drs/internal/planner"
	"github.com/zhanwei-ai/kubevirt-drs/internal/scorer"
	"github.com/zhanwei-ai/kubevirt-drs/internal/tainter"
	"github.com/zhanwei-ai/kubevirt-drs/internal/watchdog"
)

// Cycle is the brain's loop: every DRSPolicy.spec.cycleInterval it collects, decides and acts. It runs on the
// leader only (NeedLeaderElection) so two replicas never cycle at once.
type Cycle struct {
	Client client.Client
	Prom   scorer.Querier
	Exec   *executor.Executor
	Log    logr.Logger

	mu           sync.Mutex
	blacklist    map[planner.Pair]time.Time
	blacklistFor time.Duration
}

func (c *Cycle) NeedLeaderElection() bool { return true }

// Blacklist bans a (source, target) pair until `until` (the watchdog calls it after a stalled migration).
func (c *Cycle) Blacklist(p planner.Pair, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blacklist == nil {
		c.blacklist = map[planner.Pair]time.Time{}
	}
	c.blacklist[p] = until
}

// BlacklistFor returns the policy's blacklist duration as of the last cycle (default 1h).
func (c *Cycle) BlacklistFor() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.blacklistFor <= 0 {
		return time.Hour
	}
	return c.blacklistFor
}

func (c *Cycle) blacklistCopy(now time.Time) map[planner.Pair]time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[planner.Pair]time.Time{}
	for k, v := range c.blacklist {
		if v.After(now) {
			out[k] = v
		} else {
			delete(c.blacklist, k)
		}
	}
	return out
}

// Start runs cycles until ctx ends. The interval is re-read from the policy every time.
func (c *Cycle) Start(ctx context.Context) error {
	c.Log.Info("cycle runner started")
	for {
		interval, err := c.run(ctx)
		if err != nil {
			c.Log.Error(err, "cycle failed")
			metricCycles.WithLabelValues("error").Inc()
		}
		if interval <= 0 {
			interval = time.Minute
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// run executes one cycle and returns the interval to wait before the next one.
func (c *Cycle) run(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	defer func() { metricCycleDuration.Observe(time.Since(start).Seconds()) }()

	var pol drsv1alpha1.DRSPolicy
	if err := c.Client.Get(ctx, types.NamespacedName{Name: drsv1alpha1.PolicyName}, &pol); err != nil {
		if apierrors.IsNotFound(err) {
			c.Log.Info("no DRSPolicy named default: idle (create one, the chart does with policy.create)")
			metricReady.Set(0)
			return time.Minute, nil
		}
		return time.Minute, err
	}
	interval := kube.Duration(pol.Spec.CycleInterval, time.Minute)
	log := c.Log.WithValues("generation", pol.Generation)

	// ---- dependencies -> conditions -------------------------------------------------------------
	probe := scorer.RunProbe(ctx, c.Prom)
	limits, kvErr := kube.ReadLimits(ctx, c.Client)
	setCond(&pol, drsv1alpha1.ConditionPrometheusReachable, probe.Reachable, "Reachable", "Unreachable", errString(probe.Err, "Prometheus answered"))
	setCond(&pol, drsv1alpha1.ConditionScoreRuleLoaded, probe.ScoreRuleLoaded, "Loaded", "NotLoaded",
		pick(probe.ScoreRuleLoaded, "rule group "+scorer.ScoreRuleGroup+" is loaded",
			"Prometheus has not loaded rule group "+scorer.ScoreRuleGroup+": check the Prometheus Operator ruleSelector (prometheusRule.additionalLabels) and that KubeVirt is scraped (scraped="+fmt.Sprint(probe.KubeVirtScraped)+")"))
	setCond(&pol, drsv1alpha1.ConditionKubeVirtAvailable, kvErr == nil && limits.KubeVirtDeployed, "Deployed", "Unavailable", errString(kvErr, pick(limits.KubeVirtDeployed, "KubeVirt CR is Deployed", "KubeVirt CR is not Deployed")))
	ready := probe.Reachable && probe.ScoreRuleLoaded && kvErr == nil && limits.KubeVirtDeployed
	if !ready {
		setCond(&pol, drsv1alpha1.ConditionReady, false, "DependencyMissing", "DependencyMissing", "a dependency check failed; see the other conditions. No recommendation is made or applied while this holds")
		metricReady.Set(0)
		pol.Status.ObservedGeneration = pol.Generation
		pol.Status.LastCycleTime = &metav1.Time{Time: start}
		pol.Status.LastCycleDuration = &metav1.Duration{Duration: time.Since(start)}
		metricCycles.WithLabelValues("degraded").Inc()
		return interval, c.Client.Status().Update(ctx, &pol)
	}
	metricReady.Set(1)

	// ---- collect ----------------------------------------------------------------------------------
	metrics, err := scorer.Collect(ctx, c.Prom, kube.Duration(pol.Spec.Scoring.SmoothingWindow, 5*time.Minute))
	if err != nil {
		return interval, err
	}
	var vmis kubevirtv1.VirtualMachineInstanceList
	if err := c.Client.List(ctx, &vmis); err != nil {
		return interval, fmt.Errorf("list VMIs: %w", err)
	}
	var nodeList corev1.NodeList
	if err := c.Client.List(ctx, &nodeList); err != nil {
		return interval, fmt.Errorf("list nodes: %w", err)
	}
	pods, err := kube.LauncherPods(ctx, c.Client)
	if err != nil {
		return interval, err
	}
	migs, err := kube.ListMigrations(ctx, c.Client, kube.VMINodes(vmis.Items), kube.Cordoned(nodeList.Items))
	if err != nil {
		return interval, err
	}
	if _, err := c.Exec.Expire(ctx, start); err != nil {
		return interval, err
	}
	pendingRecs, err := c.Exec.Pending(ctx)
	if err != nil {
		return interval, err
	}
	pending := make(map[planner.Key]bool, len(pendingRecs))
	reservations := make([]kube.Reservation, 0, len(pendingRecs)+len(migs.Moves))
	for i := range pendingRecs {
		r := &pendingRecs[i]
		k := planner.Key{Namespace: r.Namespace, Name: r.Spec.VMI}
		pending[k] = true
		reservations = append(reservations, kube.Reservation{VMI: k, Source: r.Spec.SourceNode, Target: r.Spec.TargetNode, Pending: true})
	}
	nPending := len(pendingRecs)
	settle := kube.Duration(pol.Spec.Scoring.SmoothingWindow, 5*time.Minute)
	for _, mv := range migs.Moves {
		if mv.InFlight {
			reservations = append(reservations, kube.Reservation{VMI: mv.VMI, Source: mv.Source, Target: mv.Target})
		} else if !mv.End.IsZero() && start.Sub(mv.End) <= settle {
			reservations = append(reservations, kube.Reservation{VMI: mv.VMI, Source: mv.Source, Target: mv.Target, Done: mv.End})
		}
	}
	nsSel, vmSel, err := selectors(&pol)
	if err != nil {
		return interval, err
	}
	cooldown := planner.Cooldown{
		Base:   kube.Duration(pol.Spec.Cooldown.Base, 15*time.Minute),
		Max:    kube.Duration(pol.Spec.Cooldown.Max, 6*time.Hour),
		Window: kube.Duration(pol.Spec.Cooldown.HistoryWindow, 48*time.Hour),
	}
	vms, err := kube.BuildVMs(ctx, c.Client, vmis.Items, pods, metrics, migs, kube.BuildOptions{
		Now: start, CooldownWindow: cooldown.Window, NamespaceSelector: nsSel, VMSelector: vmSel, PendingRecs: pending,
	})
	if err != nil {
		return interval, err
	}
	nodes := kube.BuildNodes(nodeList.Items, metrics, migs)
	demandOf := map[planner.Key]float64{}
	for i := range vms {
		demandOf[vms[i].Key] = vms[i].CPUDemandCores
	}
	kube.ApplyReservations(nodes, reservations, func(k planner.Key) float64 { return demandOf[k] }, start, settle)

	// ---- watchdog: our own migrations that never found a node -----------------------------------
	blacklistFor := kube.Duration(pol.Spec.Watchdog.BlacklistFor, time.Hour)
	c.mu.Lock()
	c.blacklistFor = blacklistFor
	c.mu.Unlock()
	wd := &watchdog.Watchdog{Client: c.Client, Exec: c.Exec, Blacklist: c.Blacklist, Log: log.WithName("watchdog")}
	if n, err := wd.Run(ctx, migs.Moves, start, kube.Duration(pol.Spec.Watchdog.GracePeriod, 2*time.Minute), blacklistFor); err != nil {
		return interval, err
	} else if n > 0 {
		metricStalled.Add(float64(n))
	}

	setCond(&pol, drsv1alpha1.ConditionExternalEvacuationsDetected, migs.ExternalEvacuations > 0, "Detected", "None",
		pick(migs.ExternalEvacuations > 0,
			fmt.Sprintf("%d kubevirt-evacuation-* migration(s) in flight from nodes that are not cordoned: something other than a drain is evicting VMs (a descheduler?). Exclude kubevirt.io=virt-launcher in its DefaultEvictor", migs.ExternalEvacuations),
			"no evacuation migrations outside a drain"))

	// ---- decide -----------------------------------------------------------------------------------
	pp := planner.Policy{
		Level:                planner.LevelFor(pol.Spec.MigrationThreshold),
		HostCausedCPUReady:   percent(pol.Spec.Scoring.HostCausedCPUReadyPercent, 5),
		HostCausedReclaim:    percent(pol.Spec.Scoring.HostCausedReclaimPercent, 10),
		Cooldown:             cooldown,
		ClusterLimit:         capInt(pol.Spec.Concurrency.Cluster, limits.ParallelCluster),
		PerNodeLimit:         capInt(pol.Spec.Concurrency.PerNode, limits.ParallelNode),
		InFlight:             migs.InFlight,
		TargetHeadroom:       0.8,
		BandwidthBytesPerSec: limits.BandwidthBytesPerSec,
		Blacklist:            c.blacklistCopy(start),
		SettleWindow:         settle,
		Now:                  start,
	}
	// Pending recommendations are re-validated every cycle, like VMware regenerates its list: a VM that
	// recovered, moved, or whose target filled up must not be applied by a late click.
	byKey := make(map[planner.Key]*planner.VM, len(vms))
	for i := range vms {
		byKey[vms[i].Key] = &vms[i]
	}
	if n, err := c.Exec.ExpireStale(ctx, func(rec *drsv1alpha1.DRSRecommendation) string {
		return planner.Stale(byKey[planner.Key{Namespace: rec.Namespace, Name: rec.Spec.VMI}], rec.Spec.SourceNode, rec.Spec.TargetNode, nodes, pp)
	}); err != nil {
		return interval, err
	} else if n > 0 {
		log.Info("expired stale recommendations", "count", n)
		nPending -= n
	}

	var decisions []planner.Decision
	var skips []planner.Skip
	if pol.Spec.Paused {
		log.V(1).Info("policy paused: scoring only")
	} else {
		decisions, skips = planner.Plan(vms, nodes, pp)
	}
	for _, s := range skips {
		log.V(1).Info("skip", "vmi", s.VM.String(), "reason", s.Reason)
	}

	// ---- act --------------------------------------------------------------------------------------
	approved := pol.Spec.AutomationLevel == drsv1alpha1.AutomationFullyAutomated
	ttl := kube.Duration(pol.Spec.RecommendationTTL, 10*time.Minute)
	for _, d := range decisions {
		rec, err := c.Exec.Recommend(ctx, d, approved, ttl, start)
		if err != nil {
			log.Error(err, "recommend failed", "vmi", d.VM.String())
			continue
		}
		metricRecommendations.WithLabelValues(fmt.Sprint(d.Priority), fmt.Sprint(approved)).Inc()
		log.Info("recommendation", "name", rec.Name, "vmi", d.VM.String(), "target", d.TargetNode,
			"score", d.CurrentScore, "expected", d.ExpectedScore, "priority", d.Priority, "approved", approved, "reason", d.Reason)
		nPending++
		if approved {
			if err := c.Exec.Apply(ctx, rec, start); err != nil {
				metricMigrations.WithLabelValues("error").Inc()
				log.Error(err, "apply failed", "recommendation", rec.Name)
				continue
			}
			metricMigrations.WithLabelValues(string(rec.Status.Phase)).Inc()
			nPending--
		}
	}
	if pol.Spec.ScoreAnnotation == nil || *pol.Spec.ScoreAnnotation {
		for i := range vmis.Items {
			vmi := &vmis.Items[i]
			if mm := metrics.For(planner.Key{Namespace: vmi.Namespace, Name: vmi.Name}, vmi.Status.NodeName); mm != nil && mm.HasScore {
				if err := c.Exec.SyncScoreAnnotation(ctx, vmi, mm.Score); err != nil {
					log.V(1).Info("score annotation", "vmi", vmi.Name, "err", err.Error())
				}
			}
		}
	}

	// ---- soft tainter -----------------------------------------------------------------------------
	softTainter := pol.Spec.SoftTainter != nil && *pol.Spec.SoftTainter
	hotNodes, changedTaints, err := tainter.Reconcile(ctx, c.Client, nodeList.Items, tainter.Hot(nodes, pp, tainter.Tainted(nodeList.Items)), softTainter)
	if err != nil {
		return interval, err
	}
	if changedTaints > 0 {
		log.Info("soft taints reconciled", "hot", hotNodes, "changed", changedTaints)
	}
	metricHotNodes.Set(float64(len(hotNodes)))
	pol.Status.HotNodes = hotNodes
	pol.Status.BlacklistedPairs = nil
	for pair, until := range c.blacklistCopy(start) {
		pol.Status.BlacklistedPairs = append(pol.Status.BlacklistedPairs, fmt.Sprintf("%s->%s until %s", pair.Source, pair.Target, until.UTC().Format(time.RFC3339)))
	}
	sort.Strings(pol.Status.BlacklistedPairs)

	// ---- status -----------------------------------------------------------------------------------
	setCond(&pol, drsv1alpha1.ConditionReady, true, pick(pol.Spec.Paused, "Paused", "Ready"), "", pick(pol.Spec.Paused, "dependencies healthy; spec.paused=true, scoring only", "dependencies healthy"))
	pol.Status.ObservedGeneration = pol.Generation
	pol.Status.LastCycleTime = &metav1.Time{Time: start}
	pol.Status.LastCycleDuration = &metav1.Duration{Duration: time.Since(start).Round(time.Millisecond)}
	pol.Status.PendingRecommendations = int32(nPending)
	pol.Status.InFlightMigrations = int32(migs.InFlight)
	if cs := metrics.ClusterScore(kube.VMINodes(vmis.Items)); cs >= 0 {
		v := int32(math.Round(cs))
		pol.Status.ClusterScore = &v
		metricClusterScore.Set(cs)
	} else {
		pol.Status.ClusterScore = nil
		metricClusterScore.Set(-1)
	}
	metricInFlight.Set(float64(migs.InFlight))
	metricCandidates.Set(float64(len(vms)))
	metricCycles.WithLabelValues("ok").Inc()
	if err := c.Client.Status().Update(ctx, &pol); err != nil {
		return interval, fmt.Errorf("update DRSPolicy status: %w", err)
	}
	log.V(1).Info("cycle done", "vms", len(vms), "nodes", len(nodes), "inflight", migs.InFlight, "decisions", len(decisions), "took", time.Since(start).Round(time.Millisecond))
	return interval, nil
}

func setCond(pol *drsv1alpha1.DRSPolicy, t string, ok bool, reasonTrue, reasonFalse, msg string) {
	status, reason := metav1.ConditionFalse, reasonFalse
	if ok {
		status, reason = metav1.ConditionTrue, reasonTrue
	}
	if reason == "" {
		reason = string(status)
	}
	meta.SetStatusCondition(&pol.Status.Conditions, metav1.Condition{Type: t, Status: status, Reason: reason, Message: msg, ObservedGeneration: pol.Generation})
}

func selectors(pol *drsv1alpha1.DRSPolicy) (ns, vm labels.Selector, err error) {
	if s := pol.Spec.Selector.NamespaceSelector; s != nil {
		if ns, err = metav1.LabelSelectorAsSelector(s); err != nil {
			return nil, nil, fmt.Errorf("spec.selector.namespaceSelector: %w", err)
		}
	}
	if s := pol.Spec.Selector.VMSelector; s != nil {
		if vm, err = metav1.LabelSelectorAsSelector(s); err != nil {
			return nil, nil, fmt.Errorf("spec.selector.vmSelector: %w", err)
		}
	}
	return ns, vm, nil
}

func percent(p *int32, def int32) float64 {
	if p == nil {
		return float64(def) / 100
	}
	return float64(*p) / 100
}

// capInt returns the policy's cap when set and lower than the cluster's limit, else the cluster's.
func capInt(p *int32, cluster int) int {
	if p != nil && int(*p) > 0 && (cluster <= 0 || int(*p) < cluster) {
		return int(*p)
	}
	return cluster
}

func pick(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func errString(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}
