package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const ModelAlternativesSecret = "model_alternatives_v1"

type ModelAlternativeRule struct {
	Provider         string `json:"provider"`
	Region           string `json:"region"`
	RequestedModel   string `json:"requested_model"`
	AlternativeModel string `json:"alternative_model"`
}
type ModelAlternativePolicy struct {
	Enabled bool                   `json:"enabled"`
	Rules   []ModelAlternativeRule `json:"rules"`
}
type ModelAlternativeReader interface {
	GetSecret(context.Context, string) (string, bool, error)
}

func LoadModelAlternativePolicy(ctx context.Context, reader ModelAlternativeReader) (ModelAlternativePolicy, error) {
	p := ModelAlternativePolicy{Rules: []ModelAlternativeRule{}}
	if reader == nil {
		return p, nil
	}
	raw, ok, err := reader.GetSecret(ctx, ModelAlternativesSecret)
	if err != nil {
		return p, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return p, nil
	}
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return ModelAlternativePolicy{}, err
	}
	if err = ValidateModelAlternativePolicy(p); err != nil {
		return ModelAlternativePolicy{}, err
	}
	if p.Rules == nil {
		p.Rules = []ModelAlternativeRule{}
	}
	return p, nil
}
func ValidateModelAlternativePolicy(p ModelAlternativePolicy) error {
	if len(p.Rules) > 32 {
		return fmt.Errorf("at most 32 alternative rules are allowed")
	}
	if p.Enabled && len(p.Rules) == 0 {
		return fmt.Errorf("an enabled policy needs a rule")
	}
	edges := map[string]string{}
	for _, r := range p.Rules {
		provider, region := strings.ToLower(strings.TrimSpace(r.Provider)), strings.ToLower(strings.TrimSpace(r.Region))
		if (provider != "workbuddy" || (region != "cn" && region != "global")) && (provider != "trae" || region != "cn") {
			return fmt.Errorf("unsupported provider or region")
		}
		from, to := CanonicalModelID(r.RequestedModel), CanonicalModelID(r.AlternativeModel)
		if from == "" || to == "" || from == "auto" || to == "auto" || from == to || len(from) > 128 || len(to) > 128 || strings.ContainsAny(from+to, "/ \t\r\n\x00") {
			return fmt.Errorf("models must be distinct unprefixed IDs")
		}
		key := provider + "|" + region + "|" + from
		target := provider + "|" + region + "|" + to
		if _, exists := edges[key]; exists {
			return fmt.Errorf("duplicate source model")
		}
		edges[key] = target
	}
	for start := range edges {
		seen := map[string]bool{}
		for at := start; at != ""; at = edges[at] {
			if seen[at] {
				return fmt.Errorf("alternative model cycle")
			}
			seen[at] = true
		}
	}
	return nil
}
