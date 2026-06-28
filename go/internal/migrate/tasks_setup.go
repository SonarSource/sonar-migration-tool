// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import "context"

// csvMappingTask builds a TaskDef that loads a CSV file into JSONL under the
// given task name. All generate*Mappings tasks share this structure.
func csvMappingTask(name, csvFile string) TaskDef {
	return TaskDef{
		Name: name,
		Run: func(ctx context.Context, e *Executor) error {
			return loadCSVToJSONL(e, name, csvFile)
		},
	}
}

// setupTasks returns tasks that load CSV mappings into JSONL for the migrate pipeline.
func setupTasks() []TaskDef {
	return []TaskDef{
		csvMappingTask("generateProjectMappings", "projects.csv"),
		csvMappingTask("generateProfileMappings", "profiles.csv"),
		csvMappingTask("generateGateMappings", "gates.csv"),
		csvMappingTask("generateGroupMappings", "groups.csv"),
		csvMappingTask("generateTemplateMappings", "templates.csv"),
		csvMappingTask("generatePortfolioMappings", "portfolios.csv"),
		csvMappingTask("generateOrganizationMappings", "organizations.csv"),
	}
}
