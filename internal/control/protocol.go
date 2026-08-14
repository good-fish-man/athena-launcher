// Package control exposes Athena Protocol v4 to the desktop runtime.
package control

import protocolv4 "github.com/good-fish-man/athena-protocol/protocol/v4"

const (
	Protocol          = protocolv4.Protocol
	RiskReadOnly      = protocolv4.RiskReadOnly
	RiskReversible    = protocolv4.RiskReversible
	RiskExternalWrite = protocolv4.RiskExternalWrite
	RiskSensitive     = protocolv4.RiskSensitive
	Allow             = protocolv4.Allow
	AskUser           = protocolv4.AskUser
	Block             = protocolv4.Block
)

type Attachment = protocolv4.Attachment
type EvidenceRef = protocolv4.EvidenceRef
type ErrorDetail = protocolv4.ErrorDetail
type Policy = protocolv4.Policy
type Action = protocolv4.Action
type Observation = protocolv4.Observation
type Progress = protocolv4.Progress
type Cancel = protocolv4.Cancel
type DeviceMessage = protocolv4.DeviceMessage
type CapabilityInstance = protocolv4.CapabilityInstance

var NewID = protocolv4.NewID
var DecodeStrict = protocolv4.DecodeStrict
