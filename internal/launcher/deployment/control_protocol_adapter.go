package deployment

import controlpkg "athena-launcher/internal/control"

const deviceProtocol = controlpkg.Protocol

const (
	deviceRiskReadOnly      = controlpkg.RiskReadOnly
	deviceRiskReversible    = controlpkg.RiskReversible
	deviceRiskExternalWrite = controlpkg.RiskExternalWrite
	deviceRiskSensitive     = controlpkg.RiskSensitive
)

type devicePolicy = controlpkg.Policy
type deviceAction = controlpkg.Action
type deviceObservation = controlpkg.Observation
type deviceProgress = controlpkg.Progress
type deviceCancel = controlpkg.Cancel

var newDeviceProtocolID = controlpkg.NewID
var decodeDeviceProtocol = controlpkg.DecodeStrict
