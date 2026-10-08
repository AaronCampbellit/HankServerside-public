package cloud

import (
	"maps"

	"github.com/dropfile/HankServerside/internal/assistant"
)

// Require discovery before offering exact-note tools to the model. A title is
// not a note ID, and a revision must come from an authorized observation.
// Live note authorization and revision checks remain in the tool adapter.
func executionNoteDefinitions(definitions []assistant.Definition, messages []assistant.Message) []assistant.Definition {
	ids, revisions := map[string]bool{}, map[string]bool{}
	for _, message := range messages {
		if message.Result == nil || message.Result.Error != nil || message.Result.Outcome != "confirmed" {
			continue
		}
		for _, resource := range message.Result.Resources {
			if resource.Type != "note" || resource.ID == "" {
				continue
			}
			ids[resource.ID] = true
			if resource.Revision != "" {
				revisions[resource.Revision] = true
			}
		}
	}
	filtered := make([]assistant.Definition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "notes.get" || definition.Name == "notes.append" {
			if len(ids) == 0 || (definition.Name == "notes.append" && len(revisions) == 0) {
				continue
			}
			if definition.InputSchema != nil {
				schema := *definition.InputSchema
				schema.Properties = maps.Clone(schema.Properties)
				for name, values := range map[string]map[string]bool{"note_id": ids, "revision": revisions} {
					if property := schema.Properties[name]; property != nil {
						copy := *property
						copy.Enum = executionIdentifierEnum(values)
						schema.Properties[name] = &copy
					}
				}
				definition.InputSchema = &schema
			}
		}
		filtered = append(filtered, definition)
	}
	return filtered
}
