package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"gopkg.in/yaml.v3"
)

//go:embed templates/workflows/*.yaml
var builtinWorkflows embed.FS

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Manage workflow templates",
	Long: `Manage workflow templates for multi-step processes.

Workflows are YAML templates that define an epic with dependent child tasks.
When instantiated, they create a structured set of issues with proper
dependencies, enabling agents to work through complex processes step by step.

Templates can be built-in (version-bump) or custom templates
stored in .beads/workflows/ directory.`,
}

var workflowListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available workflow templates",
	Run: func(cmd *cobra.Command, args []string) {
		workflows, err := loadAllWorkflows()
		if err != nil {
			FatalError("loading workflows: %v", err)
		}

		if jsonOutput {
			outputJSON(workflows)
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		blue := color.New(color.FgBlue).SprintFunc()
		dim := color.New(color.Faint).SprintFunc()

		// Group by source
		builtins := []*types.WorkflowTemplate{}
		customs := []*types.WorkflowTemplate{}

		for _, wf := range workflows {
			if isBuiltinWorkflow(wf.Name) {
				builtins = append(builtins, wf)
			} else {
				customs = append(customs, wf)
			}
		}

		if len(builtins) > 0 {
			fmt.Printf("%s\n", green("Built-in Workflows:"))
			for _, wf := range builtins {
				fmt.Printf("  %s\n", blue(wf.Name))
				if wf.Description != "" {
					// Show first line of description
					desc := strings.Split(wf.Description, "\n")[0]
					fmt.Printf("    %s\n", dim(desc))
				}
				fmt.Printf("    Tasks: %d\n", len(wf.Tasks))
			}
			fmt.Println()
		}

		if len(customs) > 0 {
			fmt.Printf("%s\n", green("Custom Workflows (.beads/workflows/):"))
			for _, wf := range customs {
				fmt.Printf("  %s\n", blue(wf.Name))
				if wf.Description != "" {
					desc := strings.Split(wf.Description, "\n")[0]
					fmt.Printf("    %s\n", dim(desc))
				}
				fmt.Printf("    Tasks: %d\n", len(wf.Tasks))
			}
			fmt.Println()
		}

		if len(workflows) == 0 {
			fmt.Println("No workflow templates available")
			fmt.Println("Create one in .beads/workflows/ or use built-in templates")
		}
	},
}

var workflowShowCmd = &cobra.Command{
	Use:   "show <template-name>",
	Short: "Show workflow template details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		templateName := args[0]
		wf, err := loadWorkflow(templateName)
		if err != nil {
			FatalError("%v", err)
		}

		if jsonOutput {
			outputJSON(wf)
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		blue := color.New(color.FgBlue).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()
		dim := color.New(color.Faint).SprintFunc()

		fmt.Printf("%s %s\n", green("Workflow:"), blue(wf.Name))
		if wf.Description != "" {
			fmt.Printf("\n%s\n", wf.Description)
		}

		if len(wf.Variables) > 0 {
			fmt.Printf("\n%s\n", green("Variables:"))
			for _, v := range wf.Variables {
				req := ""
				if v.Required {
					req = yellow(" (required)")
				}
				fmt.Printf("  %s%s: %s\n", blue(v.Name), req, v.Description)
				if v.Pattern != "" {
					fmt.Printf("    Pattern: %s\n", dim(v.Pattern))
				}
				if v.DefaultValue != "" {
					fmt.Printf("    Default: %s\n", dim(v.DefaultValue))
				}
				if v.DefaultCommand != "" {
					fmt.Printf("    Default command: %s\n", dim(v.DefaultCommand))
				}
			}
		}

		if len(wf.Preflight) > 0 {
			fmt.Printf("\n%s\n", green("Preflight Checks:"))
			for _, check := range wf.Preflight {
				fmt.Printf("  - %s\n", check.Message)
				fmt.Printf("    %s\n", dim(check.Command))
			}
		}

		fmt.Printf("\n%s\n", green("Epic:"))
		fmt.Printf("  Title: %s\n", wf.Epic.Title)
		if len(wf.Epic.Labels) > 0 {
			fmt.Printf("  Labels: %s\n", strings.Join(wf.Epic.Labels, ", "))
		}

		fmt.Printf("\n%s (%d total)\n", green("Tasks:"), len(wf.Tasks))
		for i, task := range wf.Tasks {
			deps := ""
			if len(task.DependsOn) > 0 {
				deps = dim(fmt.Sprintf(" (depends on: %s)", strings.Join(task.DependsOn, ", ")))
			}
			verify := ""
			if task.Verification != nil {
				verify = yellow(" [verified]")
			}
			fmt.Printf("  %d. %s%s%s\n", i+1, task.Title, deps, verify)
		}
	},
}

var workflowCreateCmd = &cobra.Command{
	Use:   "create <template-name> [--var key=value...]",
	Short: "Create workflow instance from template",
	Long: `Create a workflow instance from a template.

This creates an epic with child tasks based on the template definition.
Variables can be provided with --var flags, e.g.:
  bd workflow create version-bump --var version=0.31.0

The workflow creates hierarchical task IDs under the epic:
  bd-xyz123      (epic)
  bd-xyz123.1    (first task)
  bd-xyz123.2    (second task)
  ...

Tasks are created with dependencies as defined in the template.
Use 'bd ready' to see which tasks are ready to work on.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("workflow create")

		templateName := args[0]
		wf, err := loadWorkflow(templateName)
		if err != nil {
			FatalError("%v", err)
		}

		// Validate workflow has at least one task
		if len(wf.Tasks) == 0 {
			FatalError("workflow template '%s' has no tasks defined", templateName)
		}

		// Parse variable flags
		varFlags, _ := cmd.Flags().GetStringSlice("var")
		vars := make(map[string]string)
		for _, v := range varFlags {
			parts := strings.SplitN(v, "=", 2)
			if len(parts) != 2 {
				FatalError("invalid variable format: %s (expected key=value)", v)
			}
			vars[parts[0]] = parts[1]
		}

		// Add built-in variables
		vars["today"] = time.Now().Format("2006-01-02")
		vars["user"] = actor
		if vars["user"] == "" {
			vars["user"] = os.Getenv("USER")
		}

		// Process variables: apply defaults and validate required
		for _, v := range wf.Variables {
			if _, ok := vars[v.Name]; !ok {
				// Variable not provided
				if v.DefaultCommand != "" {
					// Run command to get default
					out, err := exec.Command("sh", "-c", v.DefaultCommand).Output()
					if err == nil {
						vars[v.Name] = strings.TrimSpace(string(out))
					}
				} else if v.DefaultValue != "" {
					vars[v.Name] = v.DefaultValue
				} else if v.Required {
					FatalError("required variable not provided: %s\n  Description: %s", v.Name, v.Description)
				}
			}

			// Validate pattern if specified
			if v.Pattern != "" && vars[v.Name] != "" {
				matched, err := regexp.MatchString(v.Pattern, vars[v.Name])
				if err != nil {
					FatalError("invalid pattern for variable %s: %v", v.Name, err)
				}
				if !matched {
					FatalError("variable %s value '%s' does not match pattern: %s", v.Name, vars[v.Name], v.Pattern)
				}
			}
		}

		// Check dry-run flag
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if dryRun {
			green := color.New(color.FgGreen).SprintFunc()
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Printf("%s Creating workflow from template: %s\n\n", yellow("[DRY RUN]"), templateName)
			fmt.Printf("%s\n", green("Variables:"))
			for k, v := range vars {
				fmt.Printf("  %s = %s\n", k, v)
			}
			fmt.Printf("\n%s %s\n", green("Epic:"), substituteVars(wf.Epic.Title, vars))
			fmt.Printf("\n%s\n", green("Tasks:"))
			for i, task := range wf.Tasks {
				fmt.Printf("  .%d %s\n", i+1, substituteVars(task.Title, vars))
			}
			return
		}

		// Run preflight checks (unless skipped)
		skipPreflight, _ := cmd.Flags().GetBool("skip-preflight")
		if len(wf.Preflight) > 0 && !skipPreflight {
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Println("Running preflight checks...")
			for _, check := range wf.Preflight {
				checkCmd := substituteVars(check.Command, vars)
				err := exec.Command("sh", "-c", checkCmd).Run()
				if err != nil {
					FatalError("preflight check failed: %s\n  Command: %s", check.Message, checkCmd)
				}
				fmt.Printf("  %s %s\n", green("✓"), check.Message)
			}
			fmt.Println()
		} else if skipPreflight && len(wf.Preflight) > 0 {
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Printf("%s Skipping preflight checks\n\n", yellow("⚠"))
		}

		// Create the workflow instance
		instance, err := createWorkflowInstance(wf, vars)
		if err != nil {
			FatalError("creating workflow: %v", err)
		}

		if jsonOutput {
			outputJSON(instance)
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		blue := color.New(color.FgBlue).SprintFunc()
		dim := color.New(color.Faint).SprintFunc()

		fmt.Printf("%s Created workflow from template: %s\n\n", green("✓"), templateName)
		fmt.Printf("Epic: %s %s\n", blue(instance.EpicID), substituteVars(wf.Epic.Title, vars))
		fmt.Printf("\nTasks:\n")

		// Show created tasks
		for i, task := range wf.Tasks {
			taskID := instance.TaskMap[task.ID]
			status := "ready"
			if len(task.DependsOn) > 0 {
				blockedBy := []string{}
				for _, dep := range task.DependsOn {
					blockedBy = append(blockedBy, instance.TaskMap[dep])
				}
				status = fmt.Sprintf("blocked by: %s", strings.Join(blockedBy, ", "))
			}
			fmt.Printf("  %s  %s %s\n", blue(taskID), substituteVars(task.Title, vars), dim("["+status+"]"))
			_ = i
		}

		fmt.Printf("\nNext: %s\n", blue("bd update "+instance.TaskMap[wf.Tasks[0].ID]+" --status in_progress"))
	},
}

var workflowStatusCmd = &cobra.Command{
	Use:   "status <epic-id>",
	Short: "Show workflow instance progress",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		epicID := args[0]

		// Resolve partial ID
		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: epicID}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				FatalError("resolving ID: %v", err)
			}
			if err := json.Unmarshal(resp.Data, &epicID); err != nil {
				FatalError("parsing response: %v", err)
			}
		} else {
			ctx := rootCtx
			resolved, err := resolvePartialID(ctx, store, epicID)
			if err != nil {
				FatalError("resolving ID: %v", err)
			}
			epicID = resolved
		}

		// Get epic and children
		var epic *types.Issue
		var children []*types.Issue

		if daemonClient != nil {
			// Get epic via Show
			showArgs := &rpc.ShowArgs{ID: epicID}
			resp, err := daemonClient.Show(showArgs)
			if err != nil {
				FatalError("getting epic: %v", err)
			}
			if err := json.Unmarshal(resp.Data, &epic); err != nil {
				FatalError("parsing epic: %v", err)
			}

			// Get children by listing with query for ID prefix
			listArgs := &rpc.ListArgs{
				Query: epicID + ".",
			}
			resp, err = daemonClient.List(listArgs)
			if err != nil {
				FatalError("getting children: %v", err)
			}
			if err := json.Unmarshal(resp.Data, &children); err != nil {
				FatalError("parsing children: %v", err)
			}
		} else {
			ctx := rootCtx
			var err error
			epic, err = store.GetIssue(ctx, epicID)
			if err != nil {
				FatalError("getting epic: %v", err)
			}
			if epic == nil {
				FatalError("epic not found: %s", epicID)
			}
			// Get children by searching for ID prefix
			children, err = store.SearchIssues(ctx, epicID+".", types.IssueFilter{})
			if err != nil {
				FatalError("getting children: %v", err)
			}
		}

		if jsonOutput {
			result := map[string]interface{}{
				"epic":     epic,
				"children": children,
			}
			outputJSON(result)
			return
		}

		green := color.New(color.FgGreen).SprintFunc()
		blue := color.New(color.FgBlue).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()
		red := color.New(color.FgRed).SprintFunc()
		dim := color.New(color.Faint).SprintFunc()

		// Count progress
		completed := 0
		inProgress := 0
		blocked := 0
		for _, child := range children {
			switch child.Status {
			case types.StatusClosed:
				completed++
			case types.StatusInProgress:
				inProgress++
			case types.StatusBlocked:
				blocked++
			}
		}

		total := len(children)
		pct := 0
		if total > 0 {
			pct = completed * 100 / total
		}

		fmt.Printf("Workflow: %s %s\n", blue(epicID), epic.Title)
		fmt.Printf("Progress: %d/%d tasks complete (%d%%)\n", completed, total, pct)
		if inProgress > 0 {
			fmt.Printf("  %s in progress\n", yellow(fmt.Sprintf("%d", inProgress)))
		}
		if blocked > 0 {
			fmt.Printf("  %s blocked\n", red(fmt.Sprintf("%d", blocked)))
		}
		fmt.Println()

		fmt.Println("Tasks:")
		for _, child := range children {
			var icon string
			var statusStr string
			switch child.Status {
			case types.StatusClosed:
				icon = green("✓")
				statusStr = "closed"
			case types.StatusInProgress:
				icon = yellow("○")
				statusStr = "in_progress"
			case types.StatusBlocked:
				icon = red("✗")
				statusStr = "blocked"
			default:
				icon = dim("◌")
				statusStr = string(child.Status)
			}
			fmt.Printf("  %s %s  %s %s\n", icon, blue(child.ID), child.Title, dim("["+statusStr+"]"))
		}

		// Show current task if any in progress
		for _, child := range children {
			if child.Status == types.StatusInProgress {
				fmt.Printf("\nCurrent task: %s \"%s\"\n", blue(child.ID), child.Title)
				break
			}
		}
	},
}

var workflowVerifyCmd = &cobra.Command{
	Use:   "verify <task-id>",
	Short: "Run verification command for a workflow task",
	Long: `Run the verification command defined for a workflow task.

This command looks up the task, finds its verification configuration,
and runs the verification command. Results are displayed but the task
status is not automatically changed - use 'bd close' to mark complete.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		taskID := args[0]

		// Resolve partial ID
		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: taskID}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				FatalError("resolving ID: %v", err)
			}
			if err := json.Unmarshal(resp.Data, &taskID); err != nil {
				FatalError("parsing response: %v", err)
			}
		} else {
			ctx := rootCtx
			resolved, err := resolvePartialID(ctx, store, taskID)
			if err != nil {
				FatalError("resolving ID: %v", err)
			}
			taskID = resolved
		}

		// Get task
		var task *types.Issue
		if daemonClient != nil {
			showArgs := &rpc.ShowArgs{ID: taskID}
			resp, err := daemonClient.Show(showArgs)
			if err != nil {
				FatalError("getting task: %v", err)
			}
			if err := json.Unmarshal(resp.Data, &task); err != nil {
				FatalError("parsing task: %v", err)
			}
		} else {
			ctx := rootCtx
			var err error
			task, err = store.GetIssue(ctx, taskID)
			if err != nil {
				FatalError("getting task: %v", err)
			}
			if task == nil {
				FatalError("task not found: %s", taskID)
			}
		}

		green := color.New(color.FgGreen).SprintFunc()
		red := color.New(color.FgRed).SprintFunc()
		blue := color.New(color.FgBlue).SprintFunc()
		dim := color.New(color.Faint).SprintFunc()

		// Look for verification in task description
		// Format: ```verify\ncommand\nexpect_exit: N\nexpect_stdout: pattern\n```
		verify := extractVerification(task.Description)
		if verify == nil || verify.Command == "" {
			fmt.Printf("No verification command found for task: %s\n", blue(taskID))
			fmt.Printf("Add a verification block to the task description:\n")
			fmt.Printf("  %s\n", dim("```verify"))
			fmt.Printf("  %s\n", dim("./scripts/check-versions.sh"))
			fmt.Printf("  %s\n", dim("expect_exit: 0"))
			fmt.Printf("  %s\n", dim("expect_stdout: success"))
			fmt.Printf("  %s\n", dim("```"))
			return
		}

		fmt.Printf("Running verification for: %s \"%s\"\n", blue(taskID), task.Title)
		fmt.Printf("Command: %s\n", dim(verify.Command))
		if verify.ExpectExit != nil {
			fmt.Printf("Expected exit: %d\n", *verify.ExpectExit)
		}
		if verify.ExpectStdout != "" {
			fmt.Printf("Expected stdout: %s\n", verify.ExpectStdout)
		}
		fmt.Println()

		// Run the command, capturing stdout if we need to check it
		execCmd := exec.Command("sh", "-c", verify.Command)
		var stdout strings.Builder
		if verify.ExpectStdout != "" {
			// Capture stdout for pattern matching, but also display it
			execCmd.Stdout = &stdout
		} else {
			execCmd.Stdout = os.Stdout
		}
		execCmd.Stderr = os.Stderr
		err := execCmd.Run()

		// Display captured stdout
		if verify.ExpectStdout != "" {
			fmt.Print(stdout.String())
		}

		fmt.Println()

		// Check exit code
		actualExit := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				actualExit = exitErr.ExitCode()
			} else {
				fmt.Printf("%s Verification failed: %v\n", red("✗"), err)
				os.Exit(1)
			}
		}

		// Check expected exit code
		if verify.ExpectExit != nil {
			if actualExit != *verify.ExpectExit {
				fmt.Printf("%s Verification failed: expected exit code %d, got %d\n",
					red("✗"), *verify.ExpectExit, actualExit)
				os.Exit(1)
			}
		} else if actualExit != 0 {
			// Default: expect exit code 0
			fmt.Printf("%s Verification failed (exit code: %d)\n", red("✗"), actualExit)
			os.Exit(1)
		}

		// Check expected stdout pattern
		if verify.ExpectStdout != "" {
			if !strings.Contains(stdout.String(), verify.ExpectStdout) {
				fmt.Printf("%s Verification failed: stdout does not contain %q\n",
					red("✗"), verify.ExpectStdout)
				os.Exit(1)
			}
		}

		fmt.Printf("%s Verification passed\n", green("✓"))
		fmt.Printf("\nTo close this task: %s\n", blue("bd close "+taskID))
	},
}

func init() {
	workflowCreateCmd.Flags().StringSlice("var", []string{}, "Variable values (key=value, repeatable)")
	workflowCreateCmd.Flags().Bool("dry-run", false, "Show what would be created without creating")
	workflowCreateCmd.Flags().Bool("skip-preflight", false, "Skip preflight checks (use with caution)")

	workflowCmd.AddCommand(workflowListCmd)
	workflowCmd.AddCommand(workflowShowCmd)
	workflowCmd.AddCommand(workflowCreateCmd)
	workflowCmd.AddCommand(workflowStatusCmd)
	workflowCmd.AddCommand(workflowVerifyCmd)
	rootCmd.AddCommand(workflowCmd)
}

// loadAllWorkflows loads both built-in and custom workflows
func loadAllWorkflows() ([]*types.WorkflowTemplate, error) {
	workflows := []*types.WorkflowTemplate{}

	// Load built-in workflows
	entries, err := builtinWorkflows.ReadDir("templates/workflows")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".yaml")
			wf, err := loadBuiltinWorkflow(name)
			if err != nil {
				continue
			}
			workflows = append(workflows, wf)
		}
	}

	// Load custom workflows from .beads/workflows/
	workflowsDir := filepath.Join(".beads", "workflows")
	if _, err := os.Stat(workflowsDir); err == nil {
		entries, err := os.ReadDir(workflowsDir)
		if err != nil {
			return nil, fmt.Errorf("reading workflows directory: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".yaml")
			wf, err := loadCustomWorkflow(name)
			if err != nil {
				continue
			}
			workflows = append(workflows, wf)
		}
	}

	return workflows, nil
}

// loadWorkflow loads a workflow by name (checks custom first, then built-in)
func loadWorkflow(name string) (*types.WorkflowTemplate, error) {
	if err := sanitizeTemplateName(name); err != nil {
		return nil, err
	}

	// Try custom workflows first
	wf, err := loadCustomWorkflow(name)
	if err == nil {
		return wf, nil
	}

	// Fall back to built-in workflows
	return loadBuiltinWorkflow(name)
}

// loadBuiltinWorkflow loads a built-in workflow template
func loadBuiltinWorkflow(name string) (*types.WorkflowTemplate, error) {
	path := fmt.Sprintf("templates/workflows/%s.yaml", name)
	data, err := builtinWorkflows.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("workflow '%s' not found", name)
	}

	var wf types.WorkflowTemplate
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parsing workflow: %w", err)
	}

	return &wf, nil
}

// loadCustomWorkflow loads a custom workflow from .beads/workflows/
func loadCustomWorkflow(name string) (*types.WorkflowTemplate, error) {
	path := filepath.Join(".beads", "workflows", name+".yaml")
	// #nosec G304 - path is sanitized via sanitizeTemplateName before calling this function
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("workflow '%s' not found", name)
	}

	var wf types.WorkflowTemplate
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parsing workflow: %w", err)
	}

	return &wf, nil
}

// isBuiltinWorkflow checks if a workflow name is a built-in workflow
func isBuiltinWorkflow(name string) bool {
	_, err := builtinWorkflows.ReadFile(fmt.Sprintf("templates/workflows/%s.yaml", name))
	return err == nil
}

// substituteVars replaces {{var}} placeholders with values
func substituteVars(s string, vars map[string]string) string {
	result := s
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}

// createWorkflowInstance creates an epic and child tasks from a workflow template
func createWorkflowInstance(wf *types.WorkflowTemplate, vars map[string]string) (*types.WorkflowInstance, error) {
	ctx := rootCtx

	// Substitute variables in epic
	epicTitle := substituteVars(wf.Epic.Title, vars)
	epicDesc := substituteVars(wf.Epic.Description, vars)
	epicLabels := make([]string, len(wf.Epic.Labels))
	for i, label := range wf.Epic.Labels {
		epicLabels[i] = substituteVars(label, vars)
	}
	// Always add workflow label
	epicLabels = append(epicLabels, "workflow")

	instance := &types.WorkflowInstance{
		TemplateName: wf.Name,
		Variables:    vars,
		TaskMap:      make(map[string]string),
	}

	if daemonClient != nil {
		// Create epic via daemon
		createArgs := &rpc.CreateArgs{
			Title:       epicTitle,
			Description: epicDesc,
			IssueType:   "epic",
			Priority:    wf.Epic.Priority,
			Labels:      epicLabels,
		}
		resp, err := daemonClient.Create(createArgs)
		if err != nil {
			return nil, fmt.Errorf("creating epic: %w", err)
		}
		var epic types.Issue
		if err := json.Unmarshal(resp.Data, &epic); err != nil {
			return nil, fmt.Errorf("parsing epic response: %w", err)
		}
		instance.EpicID = epic.ID

		// Create child tasks
		for _, task := range wf.Tasks {
			taskTitle := substituteVars(task.Title, vars)
			taskDesc := substituteVars(task.Description, vars)
			taskType := task.Type
			if taskType == "" {
				taskType = wf.Defaults.Type
				if taskType == "" {
					taskType = "task"
				}
			}
			taskPriority := task.Priority
			if taskPriority == 0 {
				taskPriority = wf.Defaults.Priority
				if taskPriority == 0 {
					taskPriority = 2
				}
			}

			// Add verification block to description if present
			if verifyBlock := buildVerificationBlock(task.Verification, vars); verifyBlock != "" {
				taskDesc += "\n\n" + verifyBlock
			}

			taskLabels := []string{"workflow"}

			createArgs := &rpc.CreateArgs{
				Title:       taskTitle,
				Description: taskDesc,
				IssueType:   taskType,
				Priority:    taskPriority,
				Parent:      instance.EpicID,
				Labels:      taskLabels,
			}
			resp, err := daemonClient.Create(createArgs)
			if err != nil {
				return nil, fmt.Errorf("creating task %s: %w", task.ID, err)
			}
			var created types.Issue
			if err := json.Unmarshal(resp.Data, &created); err != nil {
				return nil, fmt.Errorf("parsing task response: %w", err)
			}
			instance.TaskMap[task.ID] = created.ID
		}

		// Add dependencies between tasks
		for _, task := range wf.Tasks {
			if len(task.DependsOn) == 0 {
				continue
			}
			taskID := instance.TaskMap[task.ID]
			for _, depName := range task.DependsOn {
				depID := instance.TaskMap[depName]
				if depID == "" {
					fmt.Fprintf(os.Stderr, "Warning: task '%s' depends_on references unknown task ID '%s'\n", task.ID, depName)
					continue
				}
				depArgs := &rpc.DepAddArgs{
					FromID:  taskID,
					ToID:    depID,
					DepType: "blocks",
				}
				_, err := daemonClient.AddDependency(depArgs)
				if err != nil {
					// Non-fatal, just warn
					fmt.Fprintf(os.Stderr, "Warning: failed to add dependency %s -> %s: %v\n", taskID, depID, err)
				}
			}
		}
	} else {
		// Direct mode
		// Create epic
		epic := &types.Issue{
			Title:       epicTitle,
			Description: epicDesc,
			Status:      types.StatusOpen,
			Priority:    wf.Epic.Priority,
			IssueType:   types.TypeEpic,
		}
		if err := store.CreateIssue(ctx, epic, actor); err != nil {
			return nil, fmt.Errorf("creating epic: %w", err)
		}
		instance.EpicID = epic.ID

		// Add epic labels
		for _, label := range epicLabels {
			if err := store.AddLabel(ctx, epic.ID, label, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add label %s: %v\n", label, err)
			}
		}

		// Create child tasks
		for _, task := range wf.Tasks {
			taskTitle := substituteVars(task.Title, vars)
			taskDesc := substituteVars(task.Description, vars)
			taskType := task.Type
			if taskType == "" {
				taskType = wf.Defaults.Type
				if taskType == "" {
					taskType = "task"
				}
			}
			taskPriority := task.Priority
			if taskPriority == 0 {
				taskPriority = wf.Defaults.Priority
				if taskPriority == 0 {
					taskPriority = 2
				}
			}

			// Add verification block to description if present
			if verifyBlock := buildVerificationBlock(task.Verification, vars); verifyBlock != "" {
				taskDesc += "\n\n" + verifyBlock
			}

			// Get next child ID
			childID, err := store.GetNextChildID(ctx, instance.EpicID)
			if err != nil {
				return nil, fmt.Errorf("getting child ID: %w", err)
			}

			child := &types.Issue{
				ID:          childID,
				Title:       taskTitle,
				Description: taskDesc,
				Status:      types.StatusOpen,
				Priority:    taskPriority,
				IssueType:   types.IssueType(taskType),
			}
			if err := store.CreateIssue(ctx, child, actor); err != nil {
				return nil, fmt.Errorf("creating task %s: %w", task.ID, err)
			}
			instance.TaskMap[task.ID] = child.ID

			// Add parent-child dependency
			dep := &types.Dependency{
				IssueID:     child.ID,
				DependsOnID: instance.EpicID,
				Type:        types.DepParentChild,
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add parent-child dependency: %v\n", err)
			}

			// Add workflow label
			if err := store.AddLabel(ctx, child.ID, "workflow", actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to add workflow label: %v\n", err)
			}
		}

		// Add task dependencies
		for _, task := range wf.Tasks {
			if len(task.DependsOn) == 0 {
				continue
			}
			taskID := instance.TaskMap[task.ID]
			for _, depName := range task.DependsOn {
				depID := instance.TaskMap[depName]
				if depID == "" {
					fmt.Fprintf(os.Stderr, "Warning: task '%s' depends_on references unknown task ID '%s'\n", task.ID, depName)
					continue
				}
				dep := &types.Dependency{
					IssueID:     taskID,
					DependsOnID: depID,
					Type:        types.DepBlocks,
				}
				if err := store.AddDependency(ctx, dep, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add dependency %s -> %s: %v\n", taskID, depID, err)
				}
			}
		}

		markDirtyAndScheduleFlush()
	}

	return instance, nil
}

// parsedVerification holds verification data extracted from task description
type parsedVerification struct {
	Command      string
	ExpectExit   *int
	ExpectStdout string
}

// extractVerification extracts verification data from task description
// Looks for ```verify\ncommand\nexpect_exit: N\nexpect_stdout: pattern\n``` block
func extractVerification(description string) *parsedVerification {
	start := strings.Index(description, "```verify\n")
	if start == -1 {
		return nil
	}
	start += len("```verify\n")
	end := strings.Index(description[start:], "\n```")
	if end == -1 {
		return nil
	}
	content := description[start : start+end]

	result := &parsedVerification{}
	lines := strings.Split(content, "\n")

	// First line is always the command
	if len(lines) > 0 {
		result.Command = strings.TrimSpace(lines[0])
	}

	// Remaining lines are optional metadata
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "expect_exit:") {
			valStr := strings.TrimSpace(strings.TrimPrefix(line, "expect_exit:"))
			if val, err := fmt.Sscanf(valStr, "%d", new(int)); err == nil && val == 1 {
				exitCode := 0
				fmt.Sscanf(valStr, "%d", &exitCode)
				result.ExpectExit = &exitCode
			}
		} else if strings.HasPrefix(line, "expect_stdout:") {
			result.ExpectStdout = strings.TrimSpace(strings.TrimPrefix(line, "expect_stdout:"))
		}
	}

	return result
}

// buildVerificationBlock creates a verify block string from Verification data
func buildVerificationBlock(v *types.Verification, vars map[string]string) string {
	if v == nil || v.Command == "" {
		return ""
	}
	verifyCmd := substituteVars(v.Command, vars)
	block := "```verify\n" + verifyCmd
	if v.ExpectExit != nil {
		block += fmt.Sprintf("\nexpect_exit: %d", *v.ExpectExit)
	}
	if v.ExpectStdout != "" {
		block += "\nexpect_stdout: " + substituteVars(v.ExpectStdout, vars)
	}
	block += "\n```"
	return block
}

// resolvePartialID resolves a partial ID to a full ID (for direct mode)
func resolvePartialID(ctx context.Context, s storage.Storage, id string) (string, error) {
	return utils.ResolvePartialID(ctx, s, id)
}
