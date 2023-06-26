/*
Copyright 2023 Keylime Authors.

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

package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	attestationv1alpha1 "github.com/keylime/attestation-operator/api/attestation/v1alpha1"
	attestationcontroller "github.com/keylime/attestation-operator/internal/controller/attestation"
	keylimecontroller "github.com/keylime/attestation-operator/internal/controller/keylime"
	"github.com/keylime/attestation-operator/pkg/client/registrar"
	"github.com/keylime/attestation-operator/pkg/client/verifier"
	"github.com/keylime/attestation-operator/pkg/version"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(attestationv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	setupLog.Info("Attestation Operator", "info", version.Get())

	var registrarURL string
	var verifierURL string
	if val, ok := os.LookupEnv("KEYLIME_REGISTRAR_URL"); ok {
		if val == "" {
			err := fmt.Errorf("environment variable KEYLIME_REGISTRAR_URL is empty")
			setupLog.Error(err, "unable to determine URL for the keylime registrar")
			os.Exit(1)
		}
		registrarURL = val
	} else {
		err := fmt.Errorf("environment variable KEYLIME_REGISTRAR_URL not set")
		setupLog.Error(err, "unable to determine URL for the keylime registrar")
		os.Exit(1)
	}
	if val, ok := os.LookupEnv("KEYLIME_VERIFIER_URL"); ok {
		if val == "" {
			err := fmt.Errorf("environment variable KEYLIME_VERIFIER_URL is empty")
			setupLog.Error(err, "unable to determine URL for the keylime registrar")
			os.Exit(1)
		}
		verifierURL = val
	} else {
		err := fmt.Errorf("environment variable KEYLIME_VERIFIER_URL not set")
		setupLog.Error(err, "unable to determine URL for the keylime registrar")
		os.Exit(1)
	}

	// if this is not set, we will have a baked in default
	// compared to the URLs this is optional
	var registrarSynchronizerInterval time.Duration
	if val, ok := os.LookupEnv("KEYLIME_REGISTRAR_SYNCHRONIZER_INTERVAL_DURATION"); ok {
		var err error
		registrarSynchronizerInterval, err = time.ParseDuration(val)
		if err != nil {
			setupLog.Error(fmt.Errorf("environment variable KEYLIME_REGISTRAR_SYNCHRONIZER_INTERVAL_DURATION did not contain a duration string: %w", err), "unable to parse registrar synchronizer interval duration")
			os.Exit(1)
		}
	}

	// we are going to reuse this context in several places
	// so we'll create it already here
	ctx := ctrl.SetupSignalHandler()

	registrarClient, err := registrar.New(ctx, registrarURL)
	if err != nil {
		setupLog.Error(err, "failed to create registrar client")
		os.Exit(1)
	}
	verifierClient, err := verifier.New(ctx, verifierURL)
	if err != nil {
		setupLog.Error(err, "failed to create verifier client")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "3fadb0ac.keylime.dev",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&attestationcontroller.AgentReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Registrar: registrarClient,
		Verifier:  verifierClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Agent")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	// this is not a kubebuilder controller, so create it outside of the scaffold
	if err = (&keylimecontroller.RegistrarSynchronizer{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Registrar:    registrarClient,
		Verifier:     verifierClient,
		LoopInterval: registrarSynchronizerInterval,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RegistrarSynchronizer")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
