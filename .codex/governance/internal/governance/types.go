package governance

import "encoding/json"

const schemaVersion = 2

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
	ActivationMarker            string               `json:"activation_marker"`
	ExplicitResourceConstraints []ResourceConstraint `json:"explicit_resource_constraints"`
}

type State struct {
	SchemaVersion               int                   `json:"schema_version"`
	RunID                       string                `json:"run_id"`
	Status                      string                `json:"status"`
	AuthorityHash               string                `json:"authority_hash"`
	CompletionConditions        []CompletionCondition `json:"completion_conditions"`
	CurrentFocus                *ExecutionSnapshot    `json:"current_focus"`
	PendingIntervention         *PendingIntervention  `json:"pending_intervention"`
	ExplicitResourceConstraints []ResourceConstraint  `json:"explicit_resource_constraints"`
	ReusableResults             []ReusableResult      `json:"reusable_results"`
	NextAction                  *string               `json:"next_action"`
	Review                      ReviewState           `json:"review"`
	Owner                       *OwnerState           `json:"owner"`
	Handoff                     *HandoffState         `json:"handoff"`
}

type OwnerState struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	OwnerEpoch     uint64 `json:"owner_epoch"`
	AcquiredAt     string `json:"acquired_at"`
}

type HandoffState struct {
	HandoffID       string `json:"handoff_id"`
	TokenHash       string `json:"token_hash"`
	SourceSessionID string `json:"source_session_id"`
	SourceEpoch     uint64 `json:"source_epoch"`
	TargetThreadID  string `json:"target_thread_id"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	ExpiresAt       string `json:"expires_at"`
}

type HandoffTicket struct {
	HandoffID string `json:"handoff_id"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
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

// ExecutionSnapshot describes the main Agent's current work. It is never a
// permission grant or a Governor-controlled scope.
type ExecutionSnapshot struct {
	FocusID            string             `json:"focus_id"`
	ConditionID        string             `json:"condition_id"`
	Objective          string             `json:"objective"`
	Value              string             `json:"value"`
	InvolvedScope      []string           `json:"involved_scope"`
	ExpectedEvidence   []string           `json:"expected_evidence"`
	EvidenceCheckpoint EvidenceCheckpoint `json:"evidence_checkpoint"`
	SnapshotHash       string             `json:"snapshot_hash"`
	StartedAt          string             `json:"started_at"`
	LastEvidenceAt     *string            `json:"last_evidence_at"`
	Checkpoint         *string            `json:"checkpoint"`
	FailureEvents      []FailureEvent     `json:"failure_events"`
	FailureRepairs     []FailureRepair    `json:"failure_repairs"`
}

type FailureEvent struct {
	EventID            string   `json:"event_id"`
	Signature          string   `json:"signature"`
	FocusID            string   `json:"focus_id"`
	SourceKind         string   `json:"source_kind"`
	SourceExecution    string   `json:"source_execution"`
	ToolUseID          string   `json:"tool_use_id"`
	RepairGeneration   int      `json:"repair_generation"`
	EvidenceHash       string   `json:"evidence_hash"`
	KnownEvidenceIDs   []string `json:"known_evidence_ids"`
	RepositoryIdentity string   `json:"repository_identity,omitempty"`
	CandidateIdentity  string   `json:"candidate_identity,omitempty"`
	ConfigIdentity     string   `json:"config_identity,omitempty"`
	RuntimeIdentity    string   `json:"runtime_identity,omitempty"`
	Trust              string   `json:"trust"`
	OccurredAt         string   `json:"occurred_at"`
}

type FailureRepair struct {
	RepairID           string   `json:"repair_id"`
	Signature          string   `json:"signature"`
	PreviousEventID    string   `json:"previous_event_id"`
	FocusID            string   `json:"focus_id"`
	RepairGeneration   int      `json:"repair_generation"`
	RepositoryIdentity string   `json:"repository_identity"`
	CandidateIdentity  string   `json:"candidate_identity"`
	ConfigIdentity     string   `json:"config_identity"`
	RuntimeIdentity    string   `json:"runtime_identity"`
	EvidenceIDs        []string `json:"evidence_ids"`
	RecordedAt         string   `json:"recorded_at"`
}

type FailureEventInput struct {
	Signature       string `json:"signature"`
	SourceKind      string `json:"source_kind"`
	SourceExecution string `json:"source_execution"`
	ToolUseID       string `json:"tool_use_id"`
	EvidenceHash    string `json:"evidence_hash"`
}

type FailureRepairInput struct {
	Signature       string   `json:"signature"`
	PreviousEventID string   `json:"previous_event_id"`
	EvidenceIDs     []string `json:"evidence_ids"`
}

type EvidenceCheckpoint struct {
	CheckpointID string `json:"checkpoint_id"`
	Description  string `json:"description"`
	Reached      bool   `json:"reached"`
}

type ReviewState struct {
	Status                string          `json:"status"`
	ReviewID              *string         `json:"review_id"`
	TriggerInstanceID     *string         `json:"trigger_instance_id"`
	FixedReviewGeneration int             `json:"fixed_review_generation"`
	ReviewSnapshotHash    *string         `json:"review_snapshot_hash"`
	Trigger               *string         `json:"trigger"`
	FeedbackPath          *string         `json:"feedback_path"`
	Response              *ReviewResponse `json:"response"`
}

type ExecutionSnapshotInput struct {
	FocusID               string   `json:"focus_id"`
	ConditionID           string   `json:"condition_id"`
	Objective             string   `json:"objective"`
	Value                 string   `json:"value"`
	InvolvedScope         []string `json:"involved_scope"`
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
	CurrentFocus              *RequestFocus        `json:"current_focus"`
	PendingIntervention       *PendingIntervention `json:"pending_intervention"`
	ResourceFacts             []ResourceFact       `json:"resource_facts"`
	RecentCheckpoint          *RecentCheckpoint    `json:"recent_checkpoint"`
	EvidenceRefs              []EvidenceReference  `json:"evidence_refs"`
	CreatedAt                 string               `json:"created_at"`
}

type ReviewTrigger struct {
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	SourceID string `json:"source_id"`
	Reason   string `json:"reason"`
}

type RepositorySnapshot struct {
	Root            string `json:"root"`
	HeadCommit      string `json:"head_commit"`
	WorkingTreeHash string `json:"working_tree_hash"`
}

type RequestFocus struct {
	FocusID               string   `json:"focus_id"`
	ConditionID           string   `json:"condition_id"`
	Objective             string   `json:"objective"`
	Value                 string   `json:"value"`
	InvolvedScope         []string `json:"involved_scope"`
	ExpectedEvidence      []string `json:"expected_evidence"`
	CheckpointID          string   `json:"checkpoint_id"`
	CheckpointDescription string   `json:"checkpoint_description"`
	SnapshotHash          string   `json:"snapshot_hash"`
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

// ReviewResult is Governor feedback. It cannot mutate execution state without
// a separate, explicit main-Agent response.
type ReviewResult struct {
	ReviewID               string                  `json:"review_id"`
	TriggerInstanceID      string                  `json:"trigger_instance_id"`
	ReviewSnapshotHash     string                  `json:"review_snapshot_hash"`
	Recommendation         string                  `json:"recommendation"`
	MacroAssessment        MacroAssessment         `json:"macro_assessment"`
	HighestPriorityGap     *string                 `json:"highest_priority_gap"`
	PathAssessment         PathAssessment          `json:"path_assessment"`
	PreservedResultIDs     []string                `json:"preserved_result_ids"`
	SuggestedInvalidations []string                `json:"suggested_invalidations"`
	ValidatedEvidenceIDs   []string                `json:"validated_evidence_ids"`
	SuggestedFocus         *ExecutionSnapshotInput `json:"suggested_focus"`
	ExternalInput          *ExternalInput          `json:"external_input"`
	Reason                 string                  `json:"reason"`
}

type ReviewResponse struct {
	ReviewID            string `json:"review_id"`
	Disposition         string `json:"disposition"`
	Reason              string `json:"reason"`
	NextValidationPoint string `json:"next_validation_point"`
	RespondedAt         string `json:"responded_at"`
}

type ReviewResponseInput struct {
	ReviewID            string `json:"review_id"`
	Disposition         string `json:"disposition"`
	Reason              string `json:"reason"`
	NextValidationPoint string `json:"next_validation_point"`
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
