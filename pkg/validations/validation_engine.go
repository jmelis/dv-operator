package validations

import (
	// Used to embed yamls by kube-linter
	"context"
	_ "embed" // nolint:golint
	"fmt"
	"os"
	"strings"

	// Import checks from DVO
	"github.com/app-sre/deployment-validation-operator/pkg/configmap"
	_ "github.com/app-sre/deployment-validation-operator/pkg/validations/all" // nolint:golint

	"golang.stackrox.io/kube-linter/pkg/checkregistry"
	"golang.stackrox.io/kube-linter/pkg/config"
	"golang.stackrox.io/kube-linter/pkg/configresolver"
	"golang.stackrox.io/kube-linter/pkg/diagnostic"

	// Import and initialize all check templates from kube-linter
	_ "golang.stackrox.io/kube-linter/pkg/templates/all" // nolint:golint

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spf13/viper"
)

var engine ValidationEngine

type ValidationEngine struct {
	config           config.Config
	registry         checkregistry.CheckRegistry
	enabledChecks    []string
	registeredChecks map[string]config.Check
	metrics          map[string]*prometheus.GaugeVec
	cmWatcher        *configmap.Watcher
}

func NewEngine(configPath string, cmw configmap.Watcher) (*ValidationEngine, error) {
	ve := &ValidationEngine{
		cmWatcher: &cmw,
	}

	err := ve.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}

	err = ve.InitRegistry()
	if err != nil {
		return nil, err
	}

	return ve, nil
}

func (ve *ValidationEngine) CheckRegistry() checkregistry.CheckRegistry {
	return ve.registry
}

func (ve *ValidationEngine) EnabledChecks() []string {
	return ve.enabledChecks
}

// Get info on config file if it exists
func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func (ve *ValidationEngine) LoadConfig(path string) error {
	if !fileExists(path) {
		log.Info(fmt.Sprintf("config file %s does not exist. Use default configuration", path))
		// TODO - This hardcode will be removed when a ConfigMap is set by default in regular installation
		ve.config.Checks.DoNotAutoAddDefaults = true
		ve.config.Checks.Include = []string{
			"host-ipc",
			"host-network",
			"host-pid",
			"non-isolated-pod",
			"pdb-max-unavailable",
			"pdb-min-available",
			"privilege-escalation-container",
			"privileged-container",
			"run-as-non-root",
			"unsafe-sysctls",
			"unset-cpu-requirements",
			"unset-memory-requirements",
		}

		return nil
	}

	v := viper.New()

	// Load Configuration
	config, err := config.Load(v, path)
	if err != nil {
		log.Error(err, "failed to load config")
		return err
	}

	ve.config = config

	return nil
}

type PrometheusRegistry interface {
	Register(prometheus.Collector) error
}

func (ve *ValidationEngine) InitRegistry() error {
	disableIncompatibleChecks(&ve.config)

	registry, err := GetKubeLinterRegistry()
	if err != nil {
		return err
	}

	if err := configresolver.LoadCustomChecksInto(&ve.config, registry); err != nil {
		log.Error(err, "failed to load custom checks")
		return err
	}

	enabledChecks, err := configresolver.GetEnabledChecksAndValidate(&ve.config, registry)
	if err != nil {
		log.Error(err, "error finding enabled validations")
		return err
	}

	// TODO - Use same approach than in prometheus package
	validationMetrics := map[string]*prometheus.GaugeVec{}
	registeredChecks := map[string]config.Check{}
	for _, checkName := range enabledChecks {
		check := registry.Load(checkName)
		if check == nil {
			return fmt.Errorf("unable to create metric for check %s", checkName)
		}
		registeredChecks[check.Spec.Name] = check.Spec
		metric := newGaugeVecMetric(
			strings.ReplaceAll(check.Spec.Name, "-", "_"),
			fmt.Sprintf("Description: %s ; Remediation: %s",
				check.Spec.Description, check.Spec.Remediation),
			[]string{"namespace_uid", "namespace", "uid", "name", "kind"},
			prometheus.Labels{
				"check_description": check.Spec.Description,
				"check_remediation": check.Spec.Remediation,
			},
		)

		validationMetrics[checkName] = metric
	}

	ve.registry = registry
	ve.enabledChecks = enabledChecks
	ve.metrics = validationMetrics
	ve.registeredChecks = registeredChecks

	engine = *ve

	return nil
}

func (ve *ValidationEngine) GetMetric(name string) *prometheus.GaugeVec {
	m, ok := ve.metrics[name]
	if !ok {
		return nil
	}
	return m
}

func (ve *ValidationEngine) DeleteMetrics(labels prometheus.Labels) {
	for _, vector := range ve.metrics {
		vector.Delete(labels)
	}
}

func (ve *ValidationEngine) ClearMetrics(reports []diagnostic.WithContext, labels prometheus.Labels) {
	// Create a list of validation names for use to delete the labels from any
	// metric which isn't in the report but for which there is a metric
	reportValidationNames := map[string]struct{}{}
	for _, report := range reports {
		reportValidationNames[report.Check] = struct{}{}
	}

	// Delete the labels for validations that aren't in the list of reports
	for metricValidationName := range ve.metrics {
		if _, ok := reportValidationNames[metricValidationName]; !ok {
			ve.metrics[metricValidationName].Delete(labels)
		}
	}
}

func (ve *ValidationEngine) GetCheckByName(name string) (config.Check, error) {
	check, ok := ve.registeredChecks[name]
	if !ok {
		return config.Check{}, fmt.Errorf("check '%s' is not registered", name)
	}
	return check, nil
}

// Start will overwrite validations if no error appears with the new config
func (ve *ValidationEngine) Start(ctx context.Context) error {
	for {
		select {
		case cfg := <-ve.cmWatcher.ConfigChanged():
			ve.config = cfg
			err := ve.InitRegistry()
			if err != nil {
				fmt.Printf("error updating configuration from ConfigMap: %v\n", cfg)
			}

		case <-ctx.Done():
			return nil
		}
	}
}

// disableIncompatibleChecks will forcibly update a kube-linter config
// to disable checks that are incompatible with DVO.
// the same check name may end up in the exclude list multiple times as a result of this; this is OK.
func disableIncompatibleChecks(c *config.Config) {
	c.Checks.Exclude = append(c.Checks.Exclude, getIncompatibleChecks()...)
}

// getIncompatibleChecks returns an array of kube-linter check names that are incompatible with DVO
// these checks involve kube-linter comparing properties from multiple kubernetes objects at once.
// (e.g. "non-existent-service-account" checks that all serviceaccounts referenced by deployment objects
// exist as serviceaccount objects).
// DVO currently only performs a check against a single kubernetes object at a time,
// so these checks that compare multiple objects together will always fail.
func getIncompatibleChecks() []string {
	return []string{
		"dangling-service",
		"non-existent-service-account",
		//"non-isolated-pod",
	}
}
