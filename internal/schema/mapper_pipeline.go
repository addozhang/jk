package schema

// This file owns the Jenkins-JSON -> schema.* mapping for pipeline-level
// responses. Mappers are pure functions of []byte: they do no IO, take
// no context, and emit a fully-populated schema struct (or a wrapped
// json error).
//
// Design notes (see openspec/changes/init-jk-jenkins-cli/design.md §D7
// and tasks.md §12):
//
//   - Jenkins identifies job kinds via the `_class` field. The set of
//     classes we recognize for folders is small and well-known; anything
//     else falls back to PIPELINE (the safe default — a runnable thing
//     the user can address).
//
//   - WorkflowMultiBranchProject is rendered as Type=FOLDER per
//     docs/schema.md §3.5, because its `jobs` are branch jobs that the
//     user navigates into.
//
//   - Empty collections marshal as `[]` per the schema contract, so we
//     initialize slices to a non-nil empty value before returning.
//
//   - Nullable scalars use *T so they marshal as JSON `null` when
//     absent.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MapPipelineInfo converts a `<pipeline>/api/json` response into a
// schema.PipelineInfo. Multibranch parents contribute their child
// branch list via the `jobs` field; single-branch pipelines leave
// Branches as nil (which serializes to JSON null per the schema).
func MapPipelineInfo(raw []byte) (PipelineInfo, error) {
	var src struct {
		Class       string  `json:"_class"`
		Name        string  `json:"name"`
		FullName    string  `json:"fullName"`
		URL         string  `json:"url"`
		Description *string `json:"description"`
		Buildable   bool    `json:"buildable"`
		LastBuild   *struct {
			Number int          `json:"number"`
			URL    string       `json:"url"`
			Result *BuildResult `json:"result"`
		} `json:"lastBuild"`
		// Jobs is populated for multibranch parents; we use it to
		// enumerate branches.
		Jobs []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		return PipelineInfo{}, fmt.Errorf("MapPipelineInfo: %w", err)
	}

	out := PipelineInfo{
		Name:        src.Name,
		FullName:    src.FullName,
		URL:         src.URL,
		Description: src.Description,
		Buildable:   src.Buildable,
	}
	if src.LastBuild != nil {
		out.LastBuild = &BuildRef{
			Number: src.LastBuild.Number,
			URL:    src.LastBuild.URL,
			Result: src.LastBuild.Result,
		}
	}
	if isMultibranchClass(src.Class) {
		// Multibranch parents have no builds of their own; expose
		// branches via the Branches field.
		out.LastBuild = nil
		branches := make([]BranchRef, 0, len(src.Jobs))
		for _, j := range src.Jobs {
			branches = append(branches, BranchRef{Name: j.Name, URL: j.URL})
		}
		out.Branches = branches
	}
	return out, nil
}

// MapPipelineParams converts a tree-filtered
// `<pipeline>/api/json?tree=property[...]` response into a
// schema.PipelineParams. Definitions outside ParametersDefinitionProperty
// (e.g. discardable-build property) are ignored; unknown parameter
// classes degrade to ParameterType.UNKNOWN with their declared default
// passed through unchanged.
func MapPipelineParams(raw []byte) (PipelineParams, error) {
	var src struct {
		Property []struct {
			Class                string                   `json:"_class"`
			ParameterDefinitions []rawParameterDefinition `json:"parameterDefinitions"`
		} `json:"property"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		return PipelineParams{}, fmt.Errorf("MapPipelineParams: %w", err)
	}

	params := make([]Parameter, 0)
	for _, prop := range src.Property {
		for _, def := range prop.ParameterDefinitions {
			params = append(params, def.toSchema())
		}
	}
	return PipelineParams{Parameters: params}, nil
}

// rawParameterDefinition mirrors the shape Jenkins returns for each
// ParameterDefinition. The `type` field is the simple class name (e.g.
// "StringParameterDefinition"); we map it to a ParameterType via
// classifyParameterType.
type rawParameterDefinition struct {
	Class                 string  `json:"_class"`
	Name                  string  `json:"name"`
	Description           *string `json:"description"`
	Type                  string  `json:"type"`
	DefaultParameterValue *struct {
		Value any `json:"value"`
	} `json:"defaultParameterValue"`
	Choices []string `json:"choices"`
}

func (d rawParameterDefinition) toSchema() Parameter {
	p := Parameter{
		Name:        d.Name,
		Type:        classifyParameterType(d.Type, d.Class),
		Description: d.Description,
	}
	if d.DefaultParameterValue != nil {
		p.Default = d.DefaultParameterValue.Value
	}
	// Choices only meaningful for CHOICE; for everything else leave nil
	// so the JSON renders as `null`/absent rather than `[]`.
	if p.Type == ParameterTypeChoice && d.Choices != nil {
		p.Choices = d.Choices
	}
	return p
}

// classifyParameterType maps Jenkins's parameter class names to the jk
// enum. Both the simple `type` field and the fully-qualified `_class`
// field are consulted so we still recognize the type if Jenkins ever
// stops emitting one or the other.
func classifyParameterType(typeName, className string) ParameterType {
	candidate := typeName
	if candidate == "" {
		candidate = className
	}
	switch {
	case strings.Contains(candidate, "StringParameter"):
		return ParameterTypeString
	case strings.Contains(candidate, "BooleanParameter"):
		return ParameterTypeBoolean
	case strings.Contains(candidate, "ChoiceParameter"):
		return ParameterTypeChoice
	case strings.Contains(candidate, "TextParameter"):
		return ParameterTypeText
	case strings.Contains(candidate, "PasswordParameter"):
		return ParameterTypePassword
	default:
		return ParameterTypeUnknown
	}
}

// MapPipelineList converts a folder's `api/json?tree=jobs[...]`
// response into a schema.PipelineList. Each job's _class determines
// whether it is rendered as FOLDER or PIPELINE.
func MapPipelineList(raw []byte) (PipelineList, error) {
	var src struct {
		Jobs []struct {
			Class string `json:"_class"`
			Name  string `json:"name"`
			URL   string `json:"url"`
			// Folder children may include a `lastBuild`; pipelines do too.
			LastBuild *struct {
				Number int          `json:"number"`
				URL    string       `json:"url"`
				Result *BuildResult `json:"result"`
			} `json:"lastBuild"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		return PipelineList{}, fmt.Errorf("MapPipelineList: %w", err)
	}

	items := make([]Item, 0, len(src.Jobs))
	for _, j := range src.Jobs {
		it := Item{
			Name: j.Name,
			Type: classifyItemType(j.Class),
			URL:  j.URL,
		}
		// LastBuild only makes sense for pipelines; folders never have one.
		if it.Type == ItemTypePipeline && j.LastBuild != nil {
			it.LastBuild = &BuildRef{
				Number: j.LastBuild.Number,
				URL:    j.LastBuild.URL,
				Result: j.LastBuild.Result,
			}
		}
		items = append(items, it)
	}
	return PipelineList{Items: items}, nil
}

// classifyItemType maps a Jenkins job _class to FOLDER or PIPELINE.
// Multibranch projects are FOLDER (their children are branch jobs).
// Unknown classes fall back to PIPELINE: it's the safer default for
// "thing the user can address with a URL", and the worst-case impact
// is that `jk pipeline list` shows a job we cannot drill into — which
// the user can investigate via `jk pipeline info`.
func classifyItemType(class string) ItemType {
	switch {
	case isFolderClass(class), isMultibranchClass(class):
		return ItemTypeFolder
	default:
		return ItemTypePipeline
	}
}

// isFolderClass reports whether a Jenkins _class identifies a plain
// folder (com.cloudbees.hudson.plugins.folder.Folder and its known
// subclasses).
func isFolderClass(class string) bool {
	switch class {
	case
		"com.cloudbees.hudson.plugins.folder.Folder",
		"jenkins.branch.OrganizationFolder":
		return true
	}
	return false
}

// isMultibranchClass reports whether a Jenkins _class identifies a
// multibranch pipeline project. These are rendered as FOLDER in
// PipelineList (since they contain branch jobs) but call for different
// LastBuild handling in PipelineInfo (the parent itself never builds).
func isMultibranchClass(class string) bool {
	return class == "org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject"
}

// IsFolderLikeClass reports whether the given Jenkins _class identifies
// a folder-shaped item (plain folder, organization folder, or multibranch
// project). Exposed so the CLI layer's `jk pipeline list` shape check
// uses the same classification as the mapper's PIPELINE/FOLDER decision
// — keeping one source of truth for "what counts as a folder".
func IsFolderLikeClass(class string) bool {
	return isFolderClass(class) || isMultibranchClass(class)
}
