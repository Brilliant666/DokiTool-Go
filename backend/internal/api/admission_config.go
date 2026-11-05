package api

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// PoolConfig separates a visitor's abuse budget from the service capacity.
// Rates are tokens per second. These are process-local limits, not cluster quotas.
type PoolConfig struct {
	ClientBurst       float64
	ClientRPS         float64
	GlobalBurst       float64
	GlobalRPS         float64
	ClientConcurrency int
	GlobalConcurrency int
}

type AdmissionConfig struct {
	Parse      PoolConfig
	Catalog    PoolConfig
	Relay      PoolConfig
	Reports    PoolConfig
	MaxClients int
}

// Production defaults remove the old two-request service ceiling while keeping
// the existing per-IP parse budget. Operators can tune capacity without a build.
func DefaultAdmissionConfig() AdmissionConfig {
	return AdmissionConfig{
		Parse:      PoolConfig{4, 12.0 / 60, 32, 10, 1, 16},
		Catalog:    PoolConfig{20, 5, 128, 100, 4, 64},
		Relay:      PoolConfig{12, 1, 24, 2, 2, 12},
		Reports:    PoolConfig{10, 1, 60, 5, 2, 16},
		MaxClients: 4096,
	}
}

func (c AdmissionConfig) Validate() error {
	for _, entry := range []struct {
		name string
		p    PoolConfig
	}{
		{"PARSE", c.Parse}, {"CATALOG", c.Catalog}, {"WEIBO", c.Relay}, {"REPORT", c.Reports},
	} {
		p := entry.p
		for _, x := range []struct {
			name    string
			value   float64
			minimum float64
		}{
			{"CLIENT_BURST", p.ClientBurst, 1}, {"CLIENT_RPS", p.ClientRPS, 0.001},
			{"GLOBAL_BURST", p.GlobalBurst, 1}, {"GLOBAL_RPS", p.GlobalRPS, 0.001},
		} {
			if math.IsNaN(x.value) || math.IsInf(x.value, 0) || x.value < x.minimum || x.value > 100000 {
				return fmt.Errorf("DOKITOOL_%s_%s is outside the supported positive range", entry.name, x.name)
			}
		}
		if p.ClientConcurrency < 1 || p.GlobalConcurrency < p.ClientConcurrency || p.GlobalConcurrency > 4096 {
			return fmt.Errorf("DOKITOOL_%s concurrency must satisfy 1 <= client <= global <= 4096", entry.name)
		}
	}
	// The existing relay also owns twelve binary-stream leases. Do not advertise
	// a larger outer capacity while its independent stream guard remains in place.
	if c.Relay.GlobalConcurrency > 12 {
		return fmt.Errorf("DOKITOOL_WEIBO_GLOBAL_CONCURRENCY cannot exceed the existing 12 relay stream slots")
	}
	if c.MaxClients < 1 || c.MaxClients > 65536 {
		return fmt.Errorf("DOKITOOL_ADMISSION_MAX_CLIENTS must be between 1 and 65536")
	}
	return nil
}

// LoadAdmissionConfig runs once before startup. Invalid configuration is an
// actionable startup error; zero never means accidentally unlimited traffic.
func LoadAdmissionConfig(getenv func(string) string) (AdmissionConfig, error) {
	c := DefaultAdmissionConfig()
	if getenv == nil {
		return c, fmt.Errorf("missing admission configuration reader")
	}
	for _, entry := range []struct {
		name string
		p    *PoolConfig
	}{
		{"PARSE", &c.Parse}, {"CATALOG", &c.Catalog}, {"WEIBO", &c.Relay}, {"REPORT", &c.Reports},
	} {
		p := entry.p
		for _, x := range []struct {
			name  string
			value *float64
		}{
			{"CLIENT_BURST", &p.ClientBurst}, {"CLIENT_RPS", &p.ClientRPS},
			{"GLOBAL_BURST", &p.GlobalBurst}, {"GLOBAL_RPS", &p.GlobalRPS},
		} {
			key := "DOKITOOL_" + entry.name + "_" + x.name
			if raw := strings.TrimSpace(getenv(key)); raw != "" {
				value, err := strconv.ParseFloat(raw, 64)
				if err != nil {
					return AdmissionConfig{}, fmt.Errorf("%s must be a finite positive number", key)
				}
				*x.value = value
			}
		}
		for _, x := range []struct {
			name  string
			value *int
		}{
			{"CLIENT_CONCURRENCY", &p.ClientConcurrency}, {"GLOBAL_CONCURRENCY", &p.GlobalConcurrency},
		} {
			key := "DOKITOOL_" + entry.name + "_" + x.name
			if raw := strings.TrimSpace(getenv(key)); raw != "" {
				value, err := strconv.Atoi(raw)
				if err != nil {
					return AdmissionConfig{}, fmt.Errorf("%s must be a positive integer", key)
				}
				*x.value = value
			}
		}
	}
	if raw := strings.TrimSpace(getenv("DOKITOOL_ADMISSION_MAX_CLIENTS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return AdmissionConfig{}, fmt.Errorf("DOKITOOL_ADMISSION_MAX_CLIENTS must be a positive integer")
		}
		c.MaxClients = value
	}
	return c, c.Validate()
}
