package governance

import "encoding/json"

const schemaVersion = 3

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
	SchemaVersion               int                    `json:"schema_version"`
	RunID                       string                 `json:"run_id"`
	Status                      string                 `json:"status"`
	AuthorityHash               string                 `json:"authority_hash"`
	CompletionConditions        []CompletionCondition  `json:"completion_conditions"`
	CurrentFocus                *ExecutionSnapshot     `json:"current_focus"`
	PendingIntervention         *PendingIntervention   `json:"pending_intervention"`
	ExplicitResourceConstraints []ResourceConstraint   `json:"explicit_resource_constraints"`
	ReusableResults             []ReusableResult       `json:"reusable_results"`
	NextAction                  *string                `json:"next_action"`
	ActivationSourceID          *string                `json:"activation_source_id"`
	ReviewBaseline              *ReviewBaseline        `json:"review_baseline"`
	InvalidatedEvidence         []EvidenceInvalidation `json:"invalidated_evidence"`
	LastDiagnostic              *RuntimeDiagnostic     `json:"last_diagnostic"`
	Review                      ReviewState            `json:"review"`
	Owner                       *OwnerState            `json:"owner"`
	Handoff                     *HandoffState          `json:"handoff"`
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

type RuntimeDiagnostic struct {
	Source     string `json:"source"`
	Summary    string `json:"summary"`
	OccurredAt string `json:"occurred_at"`
}

// ReviewBaseline is the last Governor input that the main Agent explicitly
// answered. It is a bounded comparison point, not a history or event log.
type ReviewBaseline struct {
	BaselineID        string                `json:"baseline_id"`
	ReviewID          string                `json:"review_id"`
	AuthorityHash     string                `json:"authority_hash"`
	FocusID           *string               `json:"focus_id"`
	FocusSnapshotHash *string               `json:"focus_snapshot_hash"`
	FocusExecutionID  *string               `json:"focus_execution_id"`
	Conditions        []CompletionCondition `json:"conditions"`
	EvidenceRefs      []EvidenceReference   `json:"evidence_refs"`
	CheckpointID      *string               `json:"checkpoint_id"`
	CheckpointOutcome string                `json:"checkpoint_outcome"`
	EstablishedAt     string                `json:"established_at"`
}

type EvidenceInvalidation struct {
	EvidenceID    string `json:"evidence_id"`
	PreviousHash  string `json:"previous_hash"`
	InvalidatedAt string `json:"invalidated_at"`
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
	ExecutionID        string             `json:"execution_id"`
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
	Response              *ReviewResponse `json:"response"`
	Pending               *PendingReview  `json:"pending"`
	GovernorAgentID       *string         `json:"governor_agent_id"`
}

// PendingReview bounds all valid events that arrive while one Review is in
// flight. It deliberately preserves only current event identities.
type PendingReview struct {
	TriggerTypes []string `json:"trigger_types"`
	SourceIDs    []string `json:"source_ids"`
	FactsHash    string   `json:"facts_hash"`
	Reason       string   `json:"reason"`
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
	AuthorityRefs             []AuthorityReference `json:"authority_refs"`
	CompletionDefinitionPaths []string             `json:"completion_definition_paths"`
	RepositorySnapshot        RepositorySnapshot   `json:"repository_snapshot"`
	StatePath                 string               `json:"state_path"`
	CurrentConditionID        *string              `json:"current_condition_id"`
	CurrentFocus              *RequestFocus        `json:"current_focus"`
	PendingIntervention       *PendingIntervention `json:"pending_intervention"`
	ResourceFacts             []ResourceFact       `json:"resource_facts"`
	RecentCheckpoint          *RecentCheckpoint    `json:"recent_checkpoint"`
	EvidenceRefs              []EvidenceReference  `json:"evidence_refs"`
	ProgressDelta             ProgressDelta        `json:"progress_delta"`
	CreatedAt                 string               `json:"created_at"`
}

type AuthorityReference struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

type ReviewTrigger struct {
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	SourceID string `json:"source_id"`
	Reason   string `json:"reason"`
}

type RepositorySnapshot struct {
	Root       string `json:"root"`
	HeadCommit string `json:"head_commit"`
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
	ExecutionID           string   `json:"execution_id"`
}

type ProgressDelta struct {
	BaselineID               *string  `json:"baseline_id"`
	CriticalConditionID      string   `json:"critical_condition_id"`
	CriticalConditionStatus  string   `json:"critical_condition_status"`
	ExpectedCheckpointID     *string  `json:"expected_checkpoint_id"`
	NewEvidenceIDs           []string `json:"new_evidence_ids"`
	ReusedEvidenceIDs        []string `json:"reused_evidence_ids"`
	InvalidatedEvidenceIDs   []string `json:"invalidated_evidence_ids"`
	ExecutionIdentityChanged bool     `json:"execution_identity_changed"`
	CheckpointOutcome        string   `json:"checkpoint_outcome"`
	NextInvestment           string   `json:"next_investment"`
	NetProgress              string   `json:"net_progress"`
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
	AuthorityClaims        []AuthorityClaim        `json:"authority_claims"`
	Assumptions            []ReviewAssumption      `json:"assumptions"`
	Reason                 string                  `json:"reason"`
}

type AuthorityClaim struct {
	Claim         string `json:"claim"`
	SourcePath    string `json:"source_path"`
	StableLocator string `json:"stable_locator"`
	SourceHash    string `json:"source_hash"`
}

type ReviewAssumption struct {
	Statement string `json:"statement"`
	Impact    string `json:"impact"`
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
	AgentID              string          `json:"agent_id"`
	AgentType            string          `json:"agent_type"`
}

type CheckpointResultInput struct {
	FocusID         string   `json:"focus_id"`
	CheckpointID    string   `json:"checkpoint_id"`
	Outcome         string   `json:"outcome"`
	EvidenceIDs     []string `json:"evidence_ids"`
	FailureCategory string   `json:"failure_category"`
	SourceExecution string   `json:"source_execution"`
	ResultHash      string   `json:"result_hash"`
}

type ExpansionReviewInput struct {
	FocusID      string   `json:"focus_id"`
	CheckpointID string   `json:"checkpoint_id"`
	EvidenceIDs  []string `json:"evidence_ids"`
	Investment   string   `json:"investment"`
}
