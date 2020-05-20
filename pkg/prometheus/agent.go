// Package prometheus implements a Prometheus-lite client for service discovery,
// scraping metrics into a WAL, and remote_write. Clients are broken into a
// set of instances, each of which contain their own set of configs.
package prometheus

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/grafana/agent/pkg/prometheus/ha"
	"github.com/grafana/agent/pkg/prometheus/ha/client"
	"github.com/grafana/agent/pkg/prometheus/instance"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/prometheus/config"
	"google.golang.org/grpc"
)

var (
	instanceAbnormalExits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_prometheus_instance_abnormal_exits_total",
		Help: "Total number of times a Prometheus instance exited unexpectedly, causing it to be restarted.",
	}, []string{"instance_name"})

	currentActiveConfigs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "agent_prometheus_active_configs",
		Help: "Current number of active configs being used by the agent.",
	})
)

var (
	DefaultConfig = Config{
		Global:                 config.DefaultGlobalConfig,
		InstanceRestartBackoff: 5 * time.Second,
	}
)

// Config defines the configuration for the entire set of Prometheus client
// instances, along with a global configuration.
type Config struct {
	Global                 config.GlobalConfig `yaml:"global"`
	WALDir                 string              `yaml:"wal_directory"`
	ServiceConfig          ha.Config           `yaml:"scraping_service"`
	ServiceClientConfig    client.Config       `yaml:"scraping_service_client"`
	Configs                []instance.Config   `yaml:"configs,omitempty"`
	InstanceRestartBackoff time.Duration       `yaml:"instance_restart_backoff,omitempty"`
}

func (c *Config) ApplyDefaults() {
	for i := range c.Configs {
		c.Configs[i].ApplyDefaults(&c.Global)
	}
}

// RegisterFlags defines flags corresponding to the Config.
func (c *Config) RegisterFlags(f *flag.FlagSet) {
	f.StringVar(&c.WALDir, "prometheus.wal-directory", "", "base directory to store the WAL in")
	f.DurationVar(&c.InstanceRestartBackoff, "prometheus.instance-restart-backoff", DefaultConfig.InstanceRestartBackoff, "how long to wait before restarting a failed Prometheus instance")

	c.ServiceConfig.RegisterFlagsWithPrefix("prometheus.service.", f)
	c.ServiceClientConfig.RegisterFlags(f)
}

// Validate checks if the Config has all required fields filled out.
func (c *Config) Validate() error {
	if c.WALDir == "" {
		return errors.New("no wal_directory configured")
	}

	usedNames := map[string]struct{}{}

	if c.ServiceConfig.Enabled && len(c.Configs) > 0 {
		return errors.New("cannot use configs when scraping_service mode is enabled")
	}

	for i, cfg := range c.Configs {
		if _, ok := usedNames[cfg.Name]; ok {
			return fmt.Errorf(
				"prometheus instance names must be unique. found multiple instances with name %s",
				cfg.Name,
			)
		}
		usedNames[cfg.Name] = struct{}{}

		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("error validating instance %d: %s", i, err)
		}
	}

	return nil
}

// Agent is an agent for collecting Prometheus metrics. It acts as a
// Prometheus-lite; only running the service discovery, remote_write,
// and WAL components of Prometheus. It is broken down into a series
// of Instances, each of which perform metric collection.
type Agent struct {
	cfg    Config
	logger log.Logger

	cm *ConfigManager

	instanceFactory instanceFactory

	ha *ha.Server
}

// New creates and starts a new Agent.
func New(cfg Config, logger log.Logger) (*Agent, error) {
	return newAgent(cfg, logger, defaultInstanceFactory)
}

func newAgent(cfg Config, logger log.Logger, fact instanceFactory) (*Agent, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	a := &Agent{
		cfg:             cfg,
		logger:          log.With(logger, "agent", "prometheus"),
		instanceFactory: fact,
	}

	a.cm = NewConfigManager(a.spawnInstance)
	for _, c := range cfg.Configs {
		a.cm.ApplyConfig(c)
	}

	if cfg.ServiceConfig.Enabled {
		var err error
		a.ha, err = ha.New(cfg.ServiceConfig, cfg.ServiceClientConfig, a.logger, a.cm)
		if err != nil {
			return nil, err
		}
	}

	return a, nil
}

// spawnInstance takes an instance.Config and launches an instance, restarting
// it if it stops unexpectedly. The instance will be stopped whenever ctx
// is canceled. This function will not return until the launched instance
// has fully shut down.
func (a *Agent) spawnInstance(ctx context.Context, c instance.Config) {
	// Make sure defaults are applied to the config in case it is
	// incomplete.
	//
	// TODO(rfratto): maybe applying defaults should happen somewhere else.
	// ConfigManager?
	c.ApplyDefaults(&a.cfg.Global)

	inst, err := a.instanceFactory(a.cfg.Global, c, a.cfg.WALDir, a.logger)
	if err != nil {
		level.Error(a.logger).Log("msg", "failed to create instance", "err", err)
		return
	}

	for {
		err = inst.Run(ctx)
		if err == nil || err != context.Canceled {
			instanceAbnormalExits.WithLabelValues(c.Name).Inc()
			level.Error(a.logger).Log("msg", "instance stopped abnormally, restarting after backoff period", "err", err, "backoff", a.cfg.InstanceRestartBackoff, "instance", c.Name)
			time.Sleep(a.cfg.InstanceRestartBackoff)
		} else {
			level.Info(a.logger).Log("msg", "stopped instance", "instance", c.Name)
			break
		}
	}
}

func (a *Agent) WireGRPC(s *grpc.Server) {
	if a.cfg.ServiceConfig.Enabled {
		a.ha.WireGRPC(s)
	}
}

// Stop stops the agent and all its instances.
func (a *Agent) Stop() {
	if a.ha != nil {
		if err := a.ha.Stop(); err != nil {
			level.Error(a.logger).Log("msg", "failed to stop scraping service server", "err", err)
		}
	}
	a.cm.Stop()
}

// inst is an interface implemented by Instance, and used by tests
// to isolate agent from instance functionality.
type inst interface {
	Run(ctx context.Context) error
}

type instanceFactory = func(global config.GlobalConfig, cfg instance.Config, walDir string, logger log.Logger) (inst, error)

func defaultInstanceFactory(global config.GlobalConfig, cfg instance.Config, walDir string, logger log.Logger) (inst, error) {
	return instance.New(global, cfg, walDir, logger)
}
