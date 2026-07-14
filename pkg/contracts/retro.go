package contracts

import "time"

// DPAStatus tracks a DPA's review state (spec §7): default-accepted, freely
// revisable up until follow-up-session creation.
type DPAStatus string

const (
	DPAAccepted DPAStatus = "accepted"
	DPARejected DPAStatus = "rejected"
	DPAEdited   DPAStatus = "edited"
)

// RemediationLayer is what a DPA proposes fixing (spec §7's DPA field table).
type RemediationLayer string

const (
	RemediationAgentConfig         RemediationLayer = "agent_config"
	RemediationToolConfig          RemediationLayer = "tool_config"
	RemediationPromptEngineering   RemediationLayer = "prompt_engineering"
	RemediationTaskDecomposition   RemediationLayer = "task_decomposition"
	RemediationDependencyStructure RemediationLayer = "dependency_structure"
	RemediationPipelineTemplate    RemediationLayer = "pipeline_template"
	RemediationHumanProcess        RemediationLayer = "human_process"
)

// RemediationLayers is the full registry, for validation and enumeration.
var RemediationLayers = []RemediationLayer{
	RemediationAgentConfig, RemediationToolConfig, RemediationPromptEngineering,
	RemediationTaskDecomposition, RemediationDependencyStructure,
	RemediationPipelineTemplate, RemediationHumanProcess,
}

// Valid reports whether r is a registered remediation layer.
func (r RemediationLayer) Valid() bool {
	for _, v := range RemediationLayers {
		if r == v {
			return true
		}
	}
	return false
}

// DPA is a Durable Preventative Action: one structured retro finding (spec
// §7). ID is unique within its owning Retro only (minted by pkg/retro as
// "dpa_1", "dpa_2", ... — DPAs are never queried outside their retro, so a
// globally unique ID buys nothing).
type DPA struct {
	ID               string           `json:"id"`
	Problem          string           `json:"problem"`
	Evidence         string           `json:"evidence"`
	RootCause        string           `json:"root_cause"`
	RemediationLayer RemediationLayer `json:"remediation_layer"`
	SuggestedAction  string           `json:"suggested_action"`
	Status           DPAStatus        `json:"status"`
}

// DPAEdit carries user edits to a DPA; empty fields are left unchanged.
type DPAEdit struct {
	Problem          string           `json:"problem,omitempty"`
	Evidence         string           `json:"evidence,omitempty"`
	RootCause        string           `json:"root_cause,omitempty"`
	RemediationLayer RemediationLayer `json:"remediation_layer,omitempty"`
	SuggestedAction  string           `json:"suggested_action,omitempty"`
}

// Retro is a persisted retrospective analysis (spec §7): the sessions it
// covered, the user's steering observations, and the DPAs it produced.
type Retro struct {
	ID                string    `json:"id"`
	SessionIDs        []string  `json:"session_ids"`
	UserObservations  string    `json:"user_observations,omitempty"`
	DPAs              []DPA     `json:"dpas"`
	FollowUpSessionID string    `json:"follow_up_session_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
