package protocol

const (
	ProtocolVersion = 1
	SchemaVersion   = "1.0"
)

const (
	ExitOK            = 0
	ExitGeneral       = 1
	ExitArguments     = 2
	ExitPrivilege     = 3
	ExitDependency    = 4
	ExitWarning       = 5
	ExitCritical      = 6
	ExitCancelled     = 7
	ExitTimeout       = 8
	ExitPartial       = 9
	ExitConfiguration = 10
)

type Category string

const (
	CategoryDiagnostic  Category = "diagnostic"
	CategoryOperational Category = "operational"
	CategoryRunbook     Category = "runbook"
)

type Manifest struct {
	ProtocolVersion int       `json:"protocol_version"`
	Name            string    `json:"name"`
	Version         string    `json:"version"`
	Description     string    `json:"description,omitempty"`
	Commands        []Command `json:"commands"`
}

type Command struct {
	Path                 []string   `json:"path"`
	Use                  string     `json:"use"`
	Short                string     `json:"short"`
	Category             Category   `json:"category"`
	Arguments            []Argument `json:"arguments"`
	Flags                []Flag     `json:"flags"`
	RequiresRoot         bool       `json:"requires_root"`
	RequiresForce        bool       `json:"requires_force"`
	SupportsDryRun       bool       `json:"supports_dry_run"`
	RequiresConfirmation bool       `json:"requires_confirmation"`
}

type Argument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Variadic    bool   `json:"variadic"`
}

type Flag struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
}

type Invocation struct {
	ProtocolVersion int            `json:"protocol_version"`
	RequestID       string         `json:"request_id"`
	CommandPath     []string       `json:"command_path"`
	Arguments       []string       `json:"arguments"`
	Options         map[string]any `json:"options"`
	Deadline        string         `json:"deadline,omitempty"`
	PlanDigest      string         `json:"plan_digest,omitempty"`
}

type Plan struct {
	CommandID            string   `json:"command_id"`
	Summary              string   `json:"summary"`
	Checks               []Check  `json:"checks"`
	Changes              []Change `json:"changes"`
	Risks                []string `json:"risks"`
	RequiresRoot         bool     `json:"requires_root"`
	RequiresForce        bool     `json:"requires_force"`
	RequiresConfirmation bool     `json:"requires_confirmation"`
}

type Status string

const (
	StatusPass      Status = "pass"
	StatusInfo      Status = "info"
	StatusWarning   Status = "warning"
	StatusCritical  Status = "critical"
	StatusSkipped   Status = "skipped"
	StatusPartial   Status = "partial"
	StatusError     Status = "error"
	StatusCancelled Status = "cancelled"
)

type ErrorKind string

const (
	ErrorGeneral       ErrorKind = "general"
	ErrorArguments     ErrorKind = "invalid_arguments"
	ErrorPrivilege     ErrorKind = "insufficient_privileges"
	ErrorDependency    ErrorKind = "missing_dependency"
	ErrorCancelled     ErrorKind = "cancelled"
	ErrorTimeout       ErrorKind = "timeout"
	ErrorConfiguration ErrorKind = "configuration"
)

type Tool struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	BuildDate    string `json:"build_date"`
	GoVersion    string `json:"go_version"`
	Architecture string `json:"architecture"`
}

type Check struct {
	ID      string         `json:"id"`
	Status  Status         `json:"status"`
	Summary string         `json:"summary"`
	Details map[string]any `json:"details,omitempty"`
}

type Change struct {
	Object  string         `json:"object"`
	Action  string         `json:"action"`
	Status  string         `json:"status"`
	Details map[string]any `json:"details,omitempty"`
}

type StructuredError struct {
	Kind       ErrorKind      `json:"kind"`
	Code       string         `json:"code,omitempty"`
	Message    string         `json:"message"`
	Dependency string         `json:"dependency,omitempty"`
	Retryable  bool           `json:"retryable"`
	Details    map[string]any `json:"details,omitempty"`
}

type Result struct {
	SchemaVersion string            `json:"schema_version"`
	Command       string            `json:"command"`
	Status        Status            `json:"status"`
	Timestamp     string            `json:"timestamp"`
	DurationMS    int64             `json:"duration_ms"`
	Host          string            `json:"host"`
	Tool          Tool              `json:"tool"`
	Checks        []Check           `json:"checks"`
	Data          map[string]any    `json:"data"`
	Changes       []Change          `json:"changes"`
	Errors        []StructuredError `json:"errors"`
}

func Normalize(input Result) Result {
	if input.SchemaVersion == "" {
		input.SchemaVersion = SchemaVersion
	}
	if input.Checks == nil {
		input.Checks = []Check{}
	}
	if input.Data == nil {
		input.Data = map[string]any{}
	}
	if input.Changes == nil {
		input.Changes = []Change{}
	}
	if input.Errors == nil {
		input.Errors = []StructuredError{}
	}
	return input
}
