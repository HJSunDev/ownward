package governance

import "encoding/json"

const schemaVersion = 1

type Config struct {
	SchemaVersion               int                  `json:"schema_version"`
	RuntimeDirectory            string               `json:"runtime_directory"`
	AuthorityPaths              []string             `json:"authority_paths"`
	CompletionDefinitionPaths   []string             `json:"completion_definition_paths"`
	EvidenceRoots               []string             `json:"evidence_roots"`
	StateSchemaPath             string               `json:"state_schema_path"`
	ReviewRequestSchemaPath     string               `json:"review_request_schema_path"`
	ReviewSchemaPath            string               `json:"review_schema_path"`
	GovernorAgentName           string               `json:"governor_agent_name"`
	GovernedToolMatcher         string               `json:"governed_tool_matcher"`
	ActivationPromptPatterns    []string             `json:"activation_prompt_patterns"`
	ExplicitResourceConstraints []ResourceConstraint `json:"explicit_resource_constraints"`
}

type State struct {
	SchemaVersion               int                   `json:"schema_version"`
	RunID                       string                `json:"run_id"`
	Status                      string                `json:"status"`
	AuthorityHash               string                `json:"authority_hash"`
	CompletionConditions        []CompletionCondition `json:"completion_conditions"`
	CurrentWorkPacket           *WorkPacket           `json:"current_work_packet"`
	PendingIntervention         *PendingIntervention  `json:"pending_intervention"`
	ExplicitResourceConstraints []ResourceConstraint  `json:"explicit_resource_constraints"`
	ReusableResults             []ReusableResult      `json:"reusable_results"`
	NextAction                  *string               `json:"next_action"`
	Review                      ReviewState           `json:"review"`
}

type CompletionCondition struct {
	ConditionID string   `json:"condition_id"`
	Status      string   `json:"status"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}

type ResourceConstraint struct {
	ConstraintID string `json:"constraint_id"`
	Source       string `json:"source"`
	Measure      string `json:"measure"`
	Limit        any    `json:"limit"`
}

type ReusableResult struct {
	ResultID     string   `json:"result_id"`
	Scope        []string `json:"scope"`
	EvidencePath string   `json:"evidence_path"`
	InputHash    string   `json:"input_hash"`
}

type WorkPacket struct {
	PacketID           string             `json:"packet_id"`
	ConditionID        string             `json:"condition_id"`
	Objective          string             `json:"objective"`
	Value              string             `json:"value"`
	AllowedScope       []string           `json:"allowed_scope"`
	ExcludedScope      []string           `json:"excluded_scope"`
	ExpectedEvidence   []string           `json:"expected_evidence"`
	EvidenceCheckpoint EvidenceCheckpoint `json:"evidence_checkpoint"`
	PlanHash           string             `json:"plan_hash"`
	Approval           *Approval          `json:"approval"`
	StartedAt          string             `json:"started_at"`
	LastEvidenceAt     *string            `json:"last_evidence_at"`
	Checkpoint         *string            `json:"checkpoint"`
	FailureSignatures  []string           `json:"failure_signatures"`
}

type EvidenceCheckpoint struct {
	CheckpointID string `json:"checkpoint_id"`
	Description  string `json:"description"`
	Reached      bool   `json:"reached"`
}

type Approval struct {
	Status               string `json:"status"`
	ReviewID             string `json:"review_id"`
	TriggerInstanceID    string `json:"trigger_instance_id"`
	ReviewSnapshotHash   string `json:"review_snapshot_hash"`
	ValidUntilCheckpoint string `json:"valid_until_checkpoint"`
}

type ReviewState struct {
	Required              bool    `json:"required"`
	ReviewID              *string `json:"review_id"`
	TriggerInstanceID     *string `json:"trigger_instance_id"`
	FixedReviewGeneration int     `json:"fixed_review_generation"`
	ReviewSnapshotHash    *string `json:"review_snapshot_hash"`
	Trigger               *string `json:"trigger"`
	DecisionPath          *string `json:"decision_path"`
}

type WorkPacketProposal struct {
	PacketID              string   `json:"packet_id"`
	ConditionID           string   `json:"condition_id"`
	Objective             string   `json:"objective"`
	Value                 string   `json:"value"`
	AllowedScope          []string `json:"allowed_scope"`
	ExcludedScope         []string `json:"excluded_scope"`
	ExpectedEvidence      []string `json:"expected_evidence"`
	CheckpointID          string   `json:"checkpoint_id"`
	CheckpointDescription string   `json:"checkpoint_description"`
}

type EvidenceRecord struct {
	EvidenceID      string   `json:"evidence_id"`
	Path            string   `json:"path"`
	Scope           []string `json:"scope"`
	InputHash       string   `json:"input_hash"`
	ValidatorStatus string   `json:"validator_status"`
	ValidatorSource string   `json:"validator_source"`
}

type ReviewRequest struct {
	SchemaVersion             int                  `json:"schema_version"`
	ReviewID                  string               `json:"review_id"`
	TriggerInstanceID         string               `json:"trigger_instance_id"`
	ReviewSnapshotHash        string               `json:"review_snapshot_hash"`
	Trigger                   ReviewTrigger        `json:"trigger"`
	AuthorityPaths            []string             `json:"authority_paths"`
	CompletionDefinitionPaths []string             `json:"completion_definition_paths"`
	RepositorySnapshot        RepositorySnapshot   `json:"repository_snapshot"`
	StatePath                 string               `json:"state_path"`
	CurrentConditionID        *string              `json:"current_condition_id"`
	CurrentWorkPacket         *RequestWorkPacket   `json:"current_work_packet"`
	PendingIntervention       *PendingIntervention `json:"pending_intervention"`
	ResourceFacts             []ResourceFact       `json:"resource_facts"`
	RecentCheckpoint          *RecentCheckpoint    `json:"recent_checkpoint"`
	EvidenceRefs              []EvidenceReference  `json:"evidence_refs"`
	CreatedAt                 string               `json:"created_at"`
}

type ReviewTrigger struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type RepositorySnapshot struct {
	Root            string `json:"root"`
	HeadCommit      string `json:"head_commit"`
	WorkingTreeHash string `json:"working_tree_hash"`
}

type RequestWorkPacket struct {
	PacketID              string   `json:"packet_id"`
	ConditionID           string   `json:"condition_id"`
	Objective             string   `json:"objective"`
	Value                 string   `json:"value"`
	AllowedScope          []string `json:"allowed_scope"`
	ExcludedScope         []string `json:"excluded_scope"`
	ExpectedEvidence      []string `json:"expected_evidence"`
	CheckpointID          string   `json:"checkpoint_id"`
	CheckpointDescription string   `json:"checkpoint_description"`
	PlanHash              string   `json:"plan_hash"`
}

type ResourceFact struct {
	Measure string `json:"measure"`
	Value   any    `json:"value"`
	Unit    string `json:"unit"`
	Source  string `json:"source"`
}

type RecentCheckpoint struct {
	CheckpointID string   `json:"checkpoint_id"`
	Description  string   `json:"description"`
	EvidenceIDs  []string `json:"evidence_ids"`
}

type EvidenceReference struct {
	EvidenceID string `json:"evidence_id"`
	Path       string `json:"path"`
	Hash       string `json:"hash"`
}

type ReviewResult struct {
	ReviewID             string              `json:"review_id"`
	TriggerInstanceID    string              `json:"trigger_instance_id"`
	ReviewSnapshotHash   string              `json:"review_snapshot_hash"`
	Decision             string              `json:"decision"`
	MacroAssessment      MacroAssessment     `json:"macro_assessment"`
	HighestPriorityGap   *string             `json:"highest_priority_gap"`
	PathAssessment       PathAssessment      `json:"path_assessment"`
	PreservedResultIDs   []string            `json:"preserved_result_ids"`
	InvalidatedItems     []string            `json:"invalidated_items"`
	ValidatedEvidenceIDs []string            `json:"validated_evidence_ids"`
	NextWorkPacket       *WorkPacketProposal `json:"next_work_packet"`
	ExternalInput        *ExternalInput      `json:"external_input"`
	Reason               string              `json:"reason"`
}

type MacroAssessment struct {
	OverallProgress string   `json:"overall_progress"`
	EvidenceSupport string   `json:"evidence_support"`
	Completed       []string `json:"completed"`
	Unmet           []string `json:"unmet"`
}

type PathAssessment struct {
	Necessary  bool     `json:"necessary"`
	Efficient  bool     `json:"efficient"`
	Optimal    bool     `json:"optimal"`
	Problems   []string `json:"problems"`
	BetterPlan []string `json:"better_plan"`
}

type ExternalInput struct {
	Kind             string   `json:"kind"`
	Fact             string   `json:"fact"`
	ExhaustedPaths   []string `json:"exhausted_paths"`
	MinimumUserInput string   `json:"minimum_user_input"`
}

type PendingIntervention struct {
	InterventionID   string                  `json:"intervention_id"`
	SourceReviewID   string                  `json:"source_review_id"`
	Kind             string                  `json:"kind"`
	Fact             string                  `json:"fact"`
	ExhaustedPaths   []string                `json:"exhausted_paths"`
	MinimumUserInput string                  `json:"minimum_user_input"`
	Status           string                  `json:"status"`
	Resolution       *InterventionResolution `json:"resolution"`
}

type InterventionResolution struct {
	SourceTurnID string   `json:"source_turn_id"`
	Summary      string   `json:"summary"`
	EvidenceRefs []string `json:"evidence_refs"`
	SubmittedAt  string   `json:"submitted_at"`
}

type ResolveInterventionInput struct {
	InterventionID string   `json:"intervention_id"`
	SourceTurnID   string   `json:"source_turn_id"`
	Summary        string   `json:"summary"`
	EvidenceRefs   []string `json:"evidence_refs"`
}

type HookInput struct {
	SessionID            string          `json:"session_id"`
	TranscriptPath       string          `json:"transcript_path"`
	CWD                  string          `json:"cwd"`
	HookEventName        string          `json:"hook_event_name"`
	Source               string          `json:"source"`
	Trigger              string          `json:"trigger"`
	TurnID               string          `json:"turn_id"`
	ToolName             string          `json:"tool_name"`
	ToolUseID            string          `json:"tool_use_id"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolResponse         json.RawMessage `json:"tool_response"`
	Prompt               string          `json:"prompt"`
	StopHookActive       bool            `json:"stop_hook_active"`
	LastAssistantMessage *string         `json:"last_assistant_message"`
}

type Event struct {
	SchemaVersion int            `json:"schema_version"`
	EventID       string         `json:"event_id"`
	OccurredAt    string         `json:"occurred_at"`
	Kind          string         `json:"kind"`
	RunID         string         `json:"run_id"`
	Summary       string         `json:"summary"`
	Fields        map[string]any `json:"fields,omitempty"`
}
