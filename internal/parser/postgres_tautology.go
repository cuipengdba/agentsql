package parser

import (
	"encoding/json"
	"fmt"
)

type postgresConstant struct {
	kind  string
	value string
}

func postgresExpressionTautology(value any) bool {
	wrapper, ok := value.(map[string]any)
	if !ok || len(wrapper) != 1 {
		return false
	}
	if expression, ok := wrapper["A_Expr"].(map[string]any); ok {
		operator, operatorOK := postgresNameListField(expression, "name")
		left, leftOK := postgresConstantValue(expression["lexpr"])
		right, rightOK := postgresConstantValue(expression["rexpr"])
		return operatorOK && operator == "=" && leftOK && rightOK && left == right
	}
	if expression, ok := wrapper["BoolExpr"].(map[string]any); ok {
		operator, _ := expression["boolop"].(string)
		arguments, _ := expression["args"].([]any)
		switch operator {
		case "OR_EXPR":
			for _, argument := range arguments {
				if postgresExpressionTautology(argument) {
					return true
				}
			}
		case "AND_EXPR":
			if len(arguments) == 0 {
				return false
			}
			for _, argument := range arguments {
				if !postgresExpressionTautology(argument) {
					return false
				}
			}
			return true
		}
	}
	if constant, ok := postgresConstantValue(value); ok {
		return constant.kind == "bool" && constant.value == "true"
	}
	return false
}

func postgresConstantValue(value any) (postgresConstant, bool) {
	wrapper, ok := value.(map[string]any)
	if !ok || len(wrapper) != 1 {
		return postgresConstant{}, false
	}
	if typeCast, ok := wrapper["TypeCast"].(map[string]any); ok {
		return postgresConstantValue(typeCast["arg"])
	}
	constant, ok := wrapper["A_Const"].(map[string]any)
	if !ok {
		if boolean, ok := wrapper["Boolean"].(map[string]any); ok {
			value, valueOK := boolean["boolval"].(bool)
			return postgresConstant{kind: "bool", value: fmt.Sprint(value)}, valueOK
		}
		return postgresConstant{}, false
	}
	if constant["isnull"] == true {
		return postgresConstant{}, false
	}
	for _, candidate := range []struct {
		field string
		kind  string
		inner string
	}{
		{field: "ival", kind: "integer", inner: "ival"},
		{field: "fval", kind: "float", inner: "fval"},
		{field: "sval", kind: "string", inner: "sval"},
		{field: "boolval", kind: "bool", inner: "boolval"},
	} {
		inner, ok := constant[candidate.field].(map[string]any)
		if !ok {
			continue
		}
		raw, ok := inner[candidate.inner]
		if !ok {
			continue
		}
		switch typed := raw.(type) {
		case string:
			return postgresConstant{kind: candidate.kind, value: typed}, true
		case bool:
			return postgresConstant{kind: candidate.kind, value: fmt.Sprint(typed)}, true
		case json.Number:
			return postgresConstant{kind: candidate.kind, value: typed.String()}, true
		}
	}
	return postgresConstant{}, false
}
