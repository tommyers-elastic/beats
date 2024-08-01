// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package fields

// dimensions

// organization dimensions
const OrganizationID = "organization_id"

// device dimensions
const DeviceModel = "device.model"

// license dimensions
const LicenseType = "license.type"
const LicenseStatus = "license.status"
const LicenseState = "license.state"
const LicenseExpirationDate = "license.expiration_date"
const LicenseExpirationState = "license.expiration_state"
const LicenseSoonestActivationDate = "license.soonest_activation_date"
const LicenseOldestActivationDate = "license.oldest_activation_date"

// metrics

// license metrics
const LicenseCount = "license.count"
const LicenseTotalCount = "license.total_count"
const LicenseUnassignedCount = "license.unassigned_count"
const LicenseExpirationThresholdDays = "license.expiration_threshold_days"
const LicenseSoonestActivationCount = "license.soonest_activation_count"
const LicenseOldestActivationCount = "license.oldest_activation_count"
const LicenseSystemsManagerTotalSeats = "license.systems_manager.total_seats"
const LicenseSystemsManagerActiveSeats = "license.systems_manager.active_seats"
const LicenseSystemsManagerUnassignedSeats = "license.systems_manager.unassigned_seats"
const LicenseSystemsManagerOrgwideEnrolledDevices = "license.systems_manager.orgwide_enrolled_devices"
