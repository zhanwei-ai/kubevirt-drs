/*
Copyright The kubevirt-drs Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command manager runs the kubevirt-drs brain: one controller that reads the DRS score from Prometheus,
// decides which VM to live-migrate where, records every decision as a DRSRecommendation and applies the
// approved ones by creating VirtualMachineInstanceMigrations.
package main

import (
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	drsv1alpha1 "github.com/zhanwei-ai/kubevirt-drs/api/v1alpha1"
	"github.com/zhanwei-ai/kubevirt-drs/internal/controller"
	"github.com/zhanwei-ai/kubevirt-drs/internal/executor"
	"github.com/zhanwei-ai/kubevirt-drs/internal/scorer"
	"github.com/zhanwei-ai/kubevirt-drs/internal/version"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(drsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(kubevirtv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		prometheusURL        string
		enableLeaderElection bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address of the /metrics endpoint")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address of the /healthz and /readyz endpoints")
	flag.StringVar(&prometheusURL, "prometheus-url", os.Getenv("DRS_PROMETHEUS_URL"),
		"base URL of the Prometheus that evaluates the kubevirt-drs recording rules (env DRS_PROMETHEUS_URL)")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"enable leader election; only needed when running more than one replica")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")
	if prometheusURL == "" {
		log.Error(nil, "--prometheus-url (or DRS_PROMETHEUS_URL) is required")
		os.Exit(2)
	}
	prom, err := scorer.NewPromClient(prometheusURL)
	if err != nil {
		log.Error(err, "prometheus client")
		os.Exit(2)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "kubevirt-drs.drs.zhanwei.ai",
		Cache: cache.Options{
			// Only virt-launcher pods are cached: they carry the scheduling constraints the planner evaluates.
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Pod{}: {Label: labels.SelectorFromSet(labels.Set{kubevirtv1.AppLabel: "virt-launcher"})},
			},
		},
	})
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}
	exec := &executor.Executor{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("kubevirt-drs")}
	cycle := &controller.Cycle{Client: mgr.GetClient(), Prom: prom, Exec: exec, Log: ctrl.Log.WithName("cycle")}
	if err := mgr.Add(cycle); err != nil {
		log.Error(err, "unable to add the cycle runner")
		os.Exit(1)
	}
	if err := (&controller.RecommendationReconciler{
		Client: mgr.GetClient(), Exec: exec, Log: ctrl.Log.WithName("recommendation"),
		Blacklist: cycle.Blacklist, BlacklistFor: cycle.BlacklistFor,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to set up the recommendation reconciler")
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	log.Info("starting kubevirt-drs manager", "version", version.Version, "prometheusURL", prometheusURL)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited with error")
		os.Exit(1)
	}
}
