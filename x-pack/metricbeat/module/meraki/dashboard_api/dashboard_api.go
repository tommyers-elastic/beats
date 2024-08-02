// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package dashboard_api

import (
	"fmt"
	"time"

	"github.com/elastic/beats/v7/libbeat/common/cfgwarn"
	"github.com/elastic/beats/v7/metricbeat/mb"
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
	// todo: device filtering?
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

	logger.Debugf("loaded config: %v", config)
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
		devices, err := getDevices(m.client, org)
		if err != nil {
			return err
		}

		deviceStatuses, err := getDeviceStatuses(m.client, org)
		if err != nil {
			return err
		}

		reportDeviceStatusMetrics(reporter, org, devices, deviceStatuses)

		uplinks, err := getDeviceUplinkMetrics(m.client, org, m.BaseMetricSet.Module().Config().Period)
		if err != nil {
			return err
		}

		reportDeviceUplinkMetrics(reporter, org, devices, uplinks)

		cotermLicenses, perDeviceLicenses, systemsManagerLicense, err := getLicenseStates(m.client, org)
		if err != nil {
			return err
		}

		reportLicenseMetrics(reporter, org, cotermLicenses, perDeviceLicenses, systemsManagerLicense)
	}

	return nil
}

func getDevices(client *meraki.Client, organizationID string) (map[Serial]*Device, error) {
	val, res, err := client.Organizations.GetOrganizationDevices(organizationID, &meraki.GetOrganizationDevicesQueryParams{})

	if err != nil {
		return nil, fmt.Errorf("GetOrganizationDevices failed; [%d] %s. %w", res.StatusCode(), res.Body(), err)
	}

	devices := make(map[Serial]*Device)
	for _, d := range *val {
		device := Device{
			Firmware:    d.Firmware,
			Imei:        d.Imei,
			LanIP:       d.LanIP,
			Location:    []*float64{d.Lng, d.Lat}, // (lon, lat) order is important!
			Mac:         d.Mac,
			Model:       d.Model,
			Name:        d.Name,
			NetworkID:   d.NetworkID,
			Notes:       d.Notes,
			ProductType: d.ProductType,
			Serial:      d.Serial,
			Tags:        d.Tags,
		}
		if d.Details != nil {
			for _, detail := range *d.Details {
				device.Details[detail.Name] = detail.Value
			}
		}
		devices[Serial(device.Serial)] = &device
	}

	return devices, nil
}

func getDeviceStatuses(client *meraki.Client, organizationID string) (map[Serial]*DeviceStatus, error) {
	val, res, err := client.Organizations.GetOrganizationDevicesStatuses(organizationID, &meraki.GetOrganizationDevicesStatusesQueryParams{})

	if err != nil {
		return nil, fmt.Errorf("GetOrganizationDevicesStatuses failed; [%d] %s. %w", res.StatusCode(), res.Body(), err)
	}

	statuses := make(map[Serial]*DeviceStatus)
	for _, status := range *val {
		statuses[Serial(status.Serial)] = &DeviceStatus{
			Gateway:        status.Gateway,
			IPType:         status.IPType,
			LastReportedAt: status.LastReportedAt,
			PrimaryDNS:     status.PrimaryDNS,
			PublicIP:       status.PublicIP,
			SecondaryDNS:   status.SecondaryDNS,
			Status:         status.Status,
		}
	}

	return statuses, nil
}

func getDeviceUplinkMetrics(client *meraki.Client, organizationID string, period time.Duration) ([]*Uplink, error) {
	val, res, err := client.Organizations.GetOrganizationDevicesUplinksLossAndLatency(
		organizationID,
		&meraki.GetOrganizationDevicesUplinksLossAndLatencyQueryParams{
			Timespan: period.Seconds() + 10, // slightly longer than the fetch period to ensure we don't miss measurements due to jitter
		},
	)

	if err != nil {
		return nil, fmt.Errorf("GetOrganizationDevicesUplinksLossAndLatency failed; [%d] %s. %w", res.StatusCode(), res.Body(), err)
	}

	var uplinks []*Uplink

	for _, device := range *val {
		uplink := &Uplink{
			DeviceSerial: Serial(device.Serial),
			IP:           device.IP,
			Interface:    device.Uplink,
		}

		for _, measurement := range *device.TimeSeries {
			if measurement.LossPercent != nil || measurement.LatencyMs != nil {
				timestamp, err := time.Parse(time.RFC3339, measurement.Ts)
				if err != nil {
					return nil, fmt.Errorf("failed to parse timestamp [%s] in ResponseOrganizationsGetOrganizationDevicesUplinksLossAndLatency: %w", measurement.Ts, err)
				}

				metric := UplinkMetric{Timestamp: timestamp}
				if measurement.LossPercent != nil {
					metric.LossPercent = measurement.LossPercent
				}
				if measurement.LatencyMs != nil {
					metric.LatencyMs = measurement.LatencyMs
				}
				uplink.Metrics = append(uplink.Metrics, &metric)
			}
		}

		if len(uplink.Metrics) != 0 {
			uplinks = append(uplinks, uplink)
		}
	}

	return uplinks, nil
}

func getLicenseStates(client *meraki.Client, organizationID string) ([]*CoterminationLicense, []*PerDeviceLicense, *SystemsManagerLicense, error) {
	val, res, err := client.Organizations.GetOrganizationLicensesOverview(organizationID)

	if err != nil {
		return nil, nil, nil, fmt.Errorf("GetOrganizationLicensesOverview failed; [%d] %s. %w", res.StatusCode(), res.Body(), err)
	}

	var cotermLicenses []*CoterminationLicense
	var perDeviceLicenses []*PerDeviceLicense
	var systemsManagerLicense *SystemsManagerLicense

	// co-termination license metrics (all devices share a single expiration date and status) are reported as counts of licenses per-device
	if val.LicensedDeviceCounts != nil {
		// i don't know why this isn't typed in the SDK - slightly worrying
		for device, count := range (*val.LicensedDeviceCounts).(map[string]interface{}) {
			cotermLicenses = append(cotermLicenses, &CoterminationLicense{
				DeviceModel:    device,
				Count:          count,
				ExpirationDate: val.ExpirationDate,
				Status:         val.Status,
			})
		}
	}

	// per-device license metrics (each device has its own expiration date and status) are reported counts of licenses per-state
	if val.States != nil {
		if val.States.Active != nil {
			perDeviceLicenses = append(perDeviceLicenses, &PerDeviceLicense{
				State: "Active",
				Count: val.States.Active.Count,
			})
		}

		if val.States.Expired != nil {
			perDeviceLicenses = append(perDeviceLicenses, &PerDeviceLicense{
				State: "Expired",
				Count: val.States.Expired.Count,
			})
		}

		if val.States.RecentlyQueued != nil {
			perDeviceLicenses = append(perDeviceLicenses, &PerDeviceLicense{
				State: "RecentlyQueued",
				Count: val.States.RecentlyQueued.Count,
			})
		}

		if val.States.Expiring != nil {
			if val.States.Expiring.Critical != nil {
				perDeviceLicenses = append(perDeviceLicenses, &PerDeviceLicense{
					State:                   "Expiring",
					ExpirationState:         "Critical",
					Count:                   val.States.Expiring.Critical.ExpiringCount,
					ExpirationThresholdDays: val.States.Expiring.Critical.ThresholdInDays,
				})
			}

			if val.States.Expiring.Warning != nil {
				perDeviceLicenses = append(perDeviceLicenses, &PerDeviceLicense{
					State:                   "Expiring",
					ExpirationState:         "Warning",
					Count:                   val.States.Expiring.Warning.ExpiringCount,
					ExpirationThresholdDays: val.States.Expiring.Warning.ThresholdInDays,
				})
			}
		}

		if val.States.Unused != nil {
			perDeviceLicenses = append(perDeviceLicenses, &PerDeviceLicense{
				State:                  "Unused",
				Count:                  val.States.Unused.Count,
				SoonestActivationDate:  val.States.Unused.SoonestActivation.ActivationDate,
				SoonestActivationCount: val.States.Unused.SoonestActivation.ToActivateCount,
			})
		}

		if val.States.UnusedActive != nil {
			perDeviceLicenses = append(perDeviceLicenses, &PerDeviceLicense{
				State:                 "UnusedActive",
				Count:                 val.States.UnusedActive.Count,
				OldestActivationDate:  val.States.UnusedActive.OldestActivation.ActivationDate,
				OldestActivationCount: val.States.UnusedActive.OldestActivation.ActiveCount,
			})
		}
	}

	if val.LicenseTypes != nil {
		for _, t := range *val.LicenseTypes {
			perDeviceLicenses = append(perDeviceLicenses, &PerDeviceLicense{
				State: "Unassigned",
				Type:  t.LicenseType,
				Count: t.Counts.Unassigned,
			})
		}
	}

	// per-device metrics also contain systems manager metrics
	if val.SystemsManager != nil {
		systemsManagerLicense = &SystemsManagerLicense{
			TotalSeats:             val.SystemsManager.Counts.TotalSeats,
			ActiveSeats:            val.SystemsManager.Counts.ActiveSeats,
			UnassignedSeats:        val.SystemsManager.Counts.UnassignedSeats,
			OrgwideEnrolledDevices: val.SystemsManager.Counts.OrgwideEnrolledDevices,
		}
	}

	return cotermLicenses, perDeviceLicenses, systemsManagerLicense, nil
}

func reportDeviceStatusMetrics(reporter mb.ReporterV2, organizationID string, devices map[Serial]*Device, deviceStatuses map[Serial]*DeviceStatus) {
	deviceStatusMetrics := []mapstr.M{}
	for serial, device := range devices {
		metric := mapstr.M{
			"device.address":      device.Address,
			"device.firmware":     device.Firmware,
			"device.imei":         device.Imei,
			"device.lan_ip":       device.LanIP,
			"device.location":     device.Location,
			"device.mac":          device.Mac,
			"device.model":        device.Model,
			"device.name":         device.Name,
			"device.network_id":   device.NetworkID,
			"device.notes":        device.Notes,
			"device.product_type": device.ProductType,
			"device.serial":       device.Serial,
			"device.tags":         device.Tags,
		}

		for k, v := range device.Details {
			metric[fmt.Sprintf("device.details.%s", k)] = v
		}

		if status, ok := deviceStatuses[serial]; ok {
			metric["device.status.gateway"] = status.Gateway
			metric["device.status.ip_type"] = status.IPType
			metric["device.status.last_reported_at"] = status.LastReportedAt
			metric["device.status.primary_dns"] = status.PrimaryDNS
			metric["device.status.public_ip"] = status.PublicIP
			metric["device.status.secondary_dns"] = status.SecondaryDNS
			metric["device.status.status"] = status.Status
		}
		deviceStatusMetrics = append(deviceStatusMetrics, metric)
	}

	reportMetricsForOrganization(reporter, organizationID, deviceStatusMetrics)
}

func reportLicenseMetrics(reporter mb.ReporterV2, organizationID string, cotermLicenses []*CoterminationLicense, perDeviceLicenses []*PerDeviceLicense, systemsManagerLicense *SystemsManagerLicense) {
	if len(cotermLicenses) != 0 {
		cotermLicenseMetrics := []mapstr.M{}
		for _, license := range cotermLicenses {
			cotermLicenseMetrics = append(cotermLicenseMetrics, mapstr.M{
				"license.device_model":    license.DeviceModel,
				"license.expiration_date": license.ExpirationDate,
				"license.status":          license.Status,
				"license.count":           license.Count,
			})
		}
		reportMetricsForOrganization(reporter, organizationID, cotermLicenseMetrics)
	}

	if len(perDeviceLicenses) != 0 {
		perDeviceLicenseMetrics := []mapstr.M{}
		for _, license := range perDeviceLicenses {
			perDeviceLicenseMetrics = append(perDeviceLicenseMetrics, mapstr.M{
				"license.state":                     license.State,
				"license.count":                     license.Count,
				"license.expiration_state":          license.ExpirationState,
				"license.expiration_threshold_days": license.ExpirationThresholdDays,
				"license.soonest_activation_date":   license.SoonestActivationDate,
				"license.soonest_activation_count":  license.SoonestActivationCount,
				"license.oldest_activation_date":    license.OldestActivationDate,
				"license.oldest_activation_count":   license.OldestActivationCount,
				"license.type":                      license.Type,
			})
		}
		reportMetricsForOrganization(reporter, organizationID, perDeviceLicenseMetrics)
	}

	if systemsManagerLicense != nil {
		reportMetricsForOrganization(reporter, organizationID, []mapstr.M{
			{
				"license.systems_manager.active_seats":            systemsManagerLicense.ActiveSeats,
				"license.systems_manager.orgwideenrolled_devices": systemsManagerLicense.OrgwideEnrolledDevices,
				"license.systems_manager.total_seats":             systemsManagerLicense.TotalSeats,
				"license.systems_manager.unassigned_seats":        systemsManagerLicense.UnassignedSeats,
			},
		})
	}
}

func reportDeviceUplinkMetrics(reporter mb.ReporterV2, organizationID string, devices map[Serial]*Device, uplinks []*Uplink) {
	metrics := []mapstr.M{}

	for _, uplink := range uplinks {
		if device, ok := devices[uplink.DeviceSerial]; ok {
			metric := mapstr.M{
				"uplink.ip":         uplink.IP,
				"upliink.interface": uplink.Interface,
				// fixme: repeated code serializing device metadata to mapstr
				"device.address":      device.Address,
				"device.firmware":     device.Firmware,
				"device.imei":         device.Imei,
				"device.lan_ip":       device.LanIP,
				"device.location":     device.Location,
				"device.mac":          device.Mac,
				"device.model":        device.Model,
				"device.name":         device.Name,
				"device.network_id":   device.NetworkID,
				"device.notes":        device.Notes,
				"device.product_type": device.ProductType,
				"device.serial":       device.Serial,
				"device.tags":         device.Tags,
			}

			for k, v := range device.Details {
				metric[fmt.Sprintf("device.details.%s", k)] = v
			}

			for _, uplinkMetric := range uplink.Metrics {
				metrics = append(metrics, mapstr.Union(metric, mapstr.M{
					"@timestamp":          uplinkMetric.Timestamp,
					"uplink.loss_percent": uplinkMetric.LossPercent,
					"uplink.latency_ms":   uplinkMetric.LatencyMs,
				}))
			}
		} else {
			// missing device metadata; ignore
		}
	}

	reportMetricsForOrganization(reporter, organizationID, metrics)
}

func reportMetricsForOrganization(reporter mb.ReporterV2, organizationID string, metrics ...[]mapstr.M) {
	for _, metricSlice := range metrics {
		for _, metric := range metricSlice {
			event := mb.Event{ModuleFields: mapstr.M{"organization_id": organizationID}}
			if ts, ok := metric["@timestamp"].(time.Time); ok {
				event.Timestamp = ts
				delete(metric, "@timestamp")
			}
			event.ModuleFields.Update(metric)
			reporter.Event(event)
		}
	}
}
