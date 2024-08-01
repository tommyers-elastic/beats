// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package dashboard_api

import (
	"fmt"

	"github.com/elastic/beats/v7/libbeat/common/cfgwarn"
	"github.com/elastic/beats/v7/metricbeat/mb"
	"github.com/elastic/beats/v7/x-pack/metricbeat/module/meraki/dashboard_api/fields"
	"github.com/elastic/elastic-agent-libs/logp"
	"github.com/elastic/elastic-agent-libs/mapstr"

	meraki "github.com/meraki/dashboard-api-go/v3/sdk"
)

// init registers the MetricSet with the central registry as soon as the program
// starts. The New function will be called later to instantiate an instance of
// the MetricSet for each host is defined in the module's configuration. After the
// MetricSet has been created then Fetch will begin to be called periodically.
func init() {
	mb.Registry.MustAddMetricSet("meraki", "dashboard_api", New)
}

type config struct {
	BaseURL       string   `config:"apiBaseURL"`
	ApiKey        string   `config:"apiKey"`
	DebugMode     string   `config:"apiDebugMode"`
	Organizations []string `config:"organizations"`
}

func defaultConfig() *config {
	return &config{
		BaseURL:   "https://api.meraki.com",
		DebugMode: "false",
	}
}

// MetricSet holds any configuration or state information. It must implement
// the mb.MetricSet interface. And this is best achieved by embedding
// mb.BaseMetricSet because it implements all of the required mb.MetricSet
// interface methods except for Fetch.
type MetricSet struct {
	mb.BaseMetricSet
	logger        *logp.Logger
	client        *meraki.Client
	organizations []string
}

// New creates a new instance of the MetricSet. New is responsible for unpacking
// any MetricSet specific configuration options if there are any.
func New(base mb.BaseMetricSet) (mb.MetricSet, error) {
	cfgwarn.Beta("The meraki dashboard_api metricset is beta.")

	logger := logp.NewLogger(base.FullyQualifiedName())

	config := defaultConfig()
	if err := base.Module().UnpackConfig(config); err != nil {
		return nil, err
	}

	logger.Debugf("config: %v", config)
	client, err := meraki.NewClientWithOptions(config.BaseURL, config.ApiKey, config.DebugMode, "Metricbeat Elastic")
	if err != nil {
		logger.Error("creating Meraki dashboard API client failed: %w", err)
		return nil, err
	}

	return &MetricSet{
		BaseMetricSet: base,
		logger:        logger,
		client:        client,
		organizations: config.Organizations,
	}, nil
}

// Fetch method implements the data gathering and data conversion to the right
// format. It publishes the event which is then forwarded to the output. In case
// of an error set the Error field of mb.Event or simply call report.Error().
func (m *MetricSet) Fetch(reporter mb.ReporterV2) error {
	for _, org := range m.organizations {
		licenseEvents, err := getLicenseStates(m.client, org)
		if err != nil {
			return err
		}

		for _, event := range licenseEvents {
			reporter.Event(mb.Event{
				ModuleFields: mapstr.Union(mapstr.M{fields.OrganizationID: org}, event),
			})
		}
	}

	return nil
}

func getLicenseStates(client *meraki.Client, organizationID string) ([]mapstr.M, error) {
	val, res, err := client.Organizations.GetOrganizationLicensesOverview(organizationID)

	if err != nil {
		return nil, fmt.Errorf("GetOrganizationLicensesOverview failed; [%d] %s. %w", res.StatusCode(), res.Body(), err)
	}

	var events []mapstr.M

	// co-termination license metrics (all devices share a single expiration date and status) are reported as counts of licenses per-device
	if val.LicensedDeviceCounts != nil {
		// i don't know why this isn't typed in the SDK - slightly worrying
		for device, count := range (*val.LicensedDeviceCounts).(map[string]interface{}) {
			events = append(events, mapstr.M{
				fields.DeviceModel:           device,
				fields.LicenseStatus:         val.Status,
				fields.LicenseExpirationDate: val.ExpirationDate,
				fields.LicenseCount:          count,
			})
		}
	}

	// per-device license metrics (each device has its own expiration date and status) are reported counts of licenses per-state
	if val.States != nil {
		if val.States.Active != nil {
			events = append(events, mapstr.M{
				fields.LicenseState: "Active",
				fields.LicenseCount: *val.States.Active.Count,
			})
		}

		if val.States.Expired != nil {
			events = append(events, mapstr.M{
				fields.LicenseState: "Expired",
				fields.LicenseCount: *val.States.Expired.Count,
			})
		}

		if val.States.RecentlyQueued != nil {
			events = append(events, mapstr.M{
				fields.LicenseState: "RecentlyQueued",
				fields.LicenseCount: *val.States.RecentlyQueued.Count,
			})
		}

		if val.States.Expiring != nil {
			if val.States.Expiring.Critical != nil {
				events = append(events, mapstr.M{
					fields.LicenseState:                   "Expiring",
					fields.LicenseExpirationState:         "Critical",
					fields.LicenseCount:                   *val.States.Expiring.Critical.ExpiringCount,
					fields.LicenseExpirationThresholdDays: *val.States.Expiring.Critical.ThresholdInDays,
				})
			}

			if val.States.Expiring.Warning != nil {
				events = append(events, mapstr.M{
					fields.LicenseState:                   "Expiring",
					fields.LicenseExpirationState:         "Warning",
					fields.LicenseCount:                   *val.States.Expiring.Warning.ExpiringCount,
					fields.LicenseExpirationThresholdDays: *val.States.Expiring.Warning.ThresholdInDays,
				})
			}
		}

		if val.States.Unused != nil {
			events = append(events, mapstr.M{
				fields.LicenseState:                  "Unused",
				fields.LicenseCount:                  *val.States.Unused.Count,
				fields.LicenseSoonestActivationDate:  val.States.Unused.SoonestActivation.ActivationDate,
				fields.LicenseSoonestActivationCount: *val.States.Unused.SoonestActivation.ToActivateCount,
			})
		}

		if val.States.UnusedActive != nil {
			events = append(events, mapstr.M{
				fields.LicenseState:                 "UnusedActive",
				fields.LicenseCount:                 *val.States.UnusedActive.Count,
				fields.LicenseOldestActivationDate:  val.States.UnusedActive.OldestActivation.ActivationDate,
				fields.LicenseOldestActivationCount: *val.States.UnusedActive.OldestActivation.ActiveCount,
			})
		}
	}

	if val.LicenseTypes != nil {
		for _, t := range *val.LicenseTypes {
			events = append(events, mapstr.M{
				fields.LicenseState: "Unassigned",
				fields.LicenseType:  t.LicenseType,
				fields.LicenseCount: *t.Counts.Unassigned,
			})
		}
	}

	// per-device metrics also contain systems manager metrics
	if val.SystemsManager != nil {
		events = append(events, mapstr.M{
			fields.LicenseTotalCount:                           *val.LicenseCount,
			fields.LicenseSystemsManagerTotalSeats:             *val.SystemsManager.Counts.TotalSeats,
			fields.LicenseSystemsManagerActiveSeats:            *val.SystemsManager.Counts.ActiveSeats,
			fields.LicenseSystemsManagerUnassignedSeats:        *val.SystemsManager.Counts.UnassignedSeats,
			fields.LicenseSystemsManagerOrgwideEnrolledDevices: *val.SystemsManager.Counts.OrgwideEnrolledDevices,
		})
	}

	return events, nil
}
