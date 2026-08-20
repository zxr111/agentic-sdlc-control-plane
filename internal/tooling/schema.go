package tooling

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// ValidateJSON enforces the bounded JSON Schema subset accepted by the Tool
// Registry. Unsupported schema keywords fail closed instead of being ignored.
func ValidateJSON(schemaRaw json.RawMessage, value any) error {
	var schema map[string]any
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return fmt.Errorf("invalid tool schema: %w", err)
	}
	return validateValue(schema, value, "$")
}

func validateValue(schema map[string]any, value any, path string) error {
	allowed := map[string]bool{"type": true, "properties": true, "required": true, "additionalProperties": true,
		"items": true, "enum": true, "minimum": true, "maximum": true, "minLength": true, "maxLength": true, "pattern": true,
		"description": true, "title": true, "$schema": true}
	for keyword := range schema {
		if !allowed[keyword] {
			return fmt.Errorf("unsupported tool schema keyword %q", keyword)
		}
	}
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, required := range stringsFromAny(schema["required"]) {
			if _, exists := object[required]; !exists {
				return fmt.Errorf("%s.%s is required", path, required)
			}
		}
		additional, hasAdditional := schema["additionalProperties"].(bool)
		for key, item := range object {
			rawProperty, exists := properties[key]
			if !exists {
				if hasAdditional && !additional {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
				continue
			}
			property, ok := rawProperty.(map[string]any)
			if !ok {
				return errors.New("tool property schema must be an object")
			}
			if err := validateValue(property, item, path+"."+key); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if rawItems, exists := schema["items"]; exists {
			items, ok := rawItems.(map[string]any)
			if !ok {
				return errors.New("tool items schema must be an object")
			}
			for index, item := range array {
				if err := validateValue(items, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if limit, ok := number(schema["minLength"]); ok && len(text) < int(limit) {
			return fmt.Errorf("%s is shorter than minLength", path)
		}
		if limit, ok := number(schema["maxLength"]); ok && len(text) > int(limit) {
			return fmt.Errorf("%s exceeds maxLength", path)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil || !compiled.MatchString(text) {
				return fmt.Errorf("%s does not match pattern", path)
			}
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s must be a number", path)
		}
	case "integer":
		numeric, ok := value.(float64)
		if !ok || numeric != float64(int64(numeric)) {
			return fmt.Errorf("%s must be an integer", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "":
	default:
		return fmt.Errorf("unsupported tool schema type %q", typeName)
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			left, _ := json.Marshal(candidate)
			right, _ := json.Marshal(value)
			if string(left) == string(right) {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("%s is not in enum", path)
		}
	}
	if numeric, ok := value.(float64); ok {
		if minimum, ok := number(schema["minimum"]); ok && numeric < minimum {
			return fmt.Errorf("%s is below minimum", path)
		}
		if maximum, ok := number(schema["maximum"]); ok && numeric > maximum {
			return fmt.Errorf("%s exceeds maximum", path)
		}
	}
	return nil
}

func stringsFromAny(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func number(value any) (float64, bool) {
	result, ok := value.(float64)
	return result, ok
}
