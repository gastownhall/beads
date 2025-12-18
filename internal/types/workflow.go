package types

// WorkflowTemplate represents a workflow definition loaded from YAML
type WorkflowTemplate struct {
	SchemaVersion int                `yaml:"schema_version" json:"schema_version"`
	Name          string             `yaml:"name" json:"name"`
	Description   string             `yaml:"description" json:"description"`
	Defaults      WorkflowDefaults   `yaml:"defaults" json:"defaults"`
	Variables     []WorkflowVariable `yaml:"variables" json:"variables"`
	Preflight     []PreflightCheck   `yaml:"preflight" json:"preflight"`
	Epic          WorkflowEpic       `yaml:"epic" json:"epic"`
	Tasks         []WorkflowTask     `yaml:"tasks" json:"tasks"`
}

// WorkflowDefaults contains default values for task fields
type WorkflowDefaults struct {
	Priority int    `yaml:"priority" json:"priority"`
	Type     string `yaml:"type" json:"type"`
}

// WorkflowVariable defines a variable that can be substituted in the template
type WorkflowVariable struct {
	Name           string `yaml:"name" json:"name"`
	Description    string `yaml:"description" json:"description"`
	Required       bool   `yaml:"required" json:"required"`
	Pattern        string `yaml:"pattern" json:"pattern"`                 // Optional regex validation
	DefaultValue   string `yaml:"default" json:"default"`                 // Static default
	DefaultCommand string `yaml:"default_command" json:"default_command"` // Command to run for default
}

// PreflightCheck is a check that must pass before workflow creation
type PreflightCheck struct {
	Command string `yaml:"command" json:"command"`
	Message string `yaml:"message" json:"message"`
}

// WorkflowEpic defines the parent epic for the workflow
type WorkflowEpic struct {
	Title       string   `yaml:"title" json:"title"`
	Description string   `yaml:"description" json:"description"`
	Priority    int      `yaml:"priority" json:"priority"`
	Labels      []string `yaml:"labels" json:"labels"`
}

// WorkflowTask defines a single task in the workflow
type WorkflowTask struct {
	ID           string        `yaml:"id" json:"id"`
	Title        string        `yaml:"title" json:"title"`
	Description  string        `yaml:"description" json:"description"`
	Type         string        `yaml:"type" json:"type"`
	Priority     int           `yaml:"priority" json:"priority"`
	Estimate     int           `yaml:"estimate" json:"estimate"` // Minutes
	DependsOn    []string      `yaml:"depends_on" json:"depends_on"`
	Verification *Verification `yaml:"verification" json:"verification"`
}

// Verification defines how to verify a task was completed successfully
type Verification struct {
	Command      string `yaml:"command" json:"command"`
	ExpectExit   *int   `yaml:"expect_exit" json:"expect_exit"`
	ExpectStdout string `yaml:"expect_stdout" json:"expect_stdout"`
}

// WorkflowInstance represents a created workflow (epic + tasks)
type WorkflowInstance struct {
	EpicID       string            `json:"epic_id"`
	TemplateName string            `json:"template_name"`
	Variables    map[string]string `json:"variables"`
	TaskMap      map[string]string `json:"task_map"` // template task ID -> actual issue ID
}
