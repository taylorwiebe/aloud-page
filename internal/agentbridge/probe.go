package agentbridge

import (
	"fmt"
	"strings"
)

type Capability string

const (
	CapabilitySameTask      Capability = "same_task"
	CapabilityWorkspace     Capability = "workspace"
	CapabilityStreaming     Capability = "streaming"
	CapabilityAuthorization Capability = "authorization"
	CapabilityCancellation  Capability = "cancellation"
	CapabilityReconnect     Capability = "reconnect"
)

type ProbeResult struct {
	Provider     string       `json:"provider"`
	TaskID       string       `json:"task_id,omitempty"`
	Supported    bool         `json:"supported"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	Reason       string       `json:"reason,omitempty"`
}

func (r ProbeResult) Has(capability Capability) bool {
	for _, candidate := range r.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func Probe(provider string, environment map[string]string) ProbeResult {
	provider = strings.ToLower(strings.TrimSpace(provider))
	result := ProbeResult{Provider: provider}
	switch provider {
	case "codex":
		taskID := strings.TrimSpace(environment["CODEX_THREAD_ID"])
		if taskID == "" {
			result.Reason = "Codex did not expose the identity of the current task"
			return result
		}
		result.TaskID = taskID
		result.Supported = true
		result.Capabilities = []Capability{
			CapabilitySameTask,
			CapabilityWorkspace,
			CapabilityStreaming,
			CapabilityAuthorization,
			CapabilityCancellation,
			CapabilityReconnect,
		}
		return result
	case "claude":
		result.Reason = "Claude same-task attachment has not passed the Planreader capability gate"
		return result
	default:
		result.Reason = fmt.Sprintf("unknown provider %q", provider)
		return result
	}
}
