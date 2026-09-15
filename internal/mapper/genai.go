package mapper

import (
	"strings"

	"github.com/doitintl/bifrost-datahub-exporter/internal/datahub"
)

// genaiDimensions emits the DoiT GenAI-intelligence system-label taxonomy
// (genai/*) so Bifrost gateway spend participates in the GenAI lens alongside the
// native provider integrations. Key set and value conventions follow DoiT's
// existing emitters (Databricks, Azure AI, Anthropic spend-report template):
// booleans are the strings "true"/"false", consumption is PAYG for
// per-token computed pricing, and model_family comes from a keyword table.
func genaiDimensions(model, userID, userEmail, keyAlias, feature string) []datahub.Dimension {
	return []datahub.Dimension{
		{Key: "genai/genai_spend", Type: "system_label", Value: "true"},
		{Key: "genai/model", Type: "system_label", Value: bareModel(model)},
		{Key: "genai/model_family", Type: "system_label", Value: modelFamily(model)},
		{Key: "genai/is_model_serving", Type: "system_label", Value: "true"},
		{Key: "genai/consumption_model", Type: "system_label", Value: "PAYG"},
		{Key: "genai/user_id", Type: "system_label", Value: userID},
		{Key: "genai/user_email", Type: "system_label", Value: userEmail},
		{Key: "genai/api_key_name", Type: "system_label", Value: keyAlias},
		{Key: "genai/feature", Type: "system_label", Value: feature},
	}
}

// bareModel strips a provider prefix when present; Bifrost models are
// usually bare already ("gpt-4o-mini"), routed prefixes stay tolerated.
func bareModel(model string) string {
	if _, rest, found := strings.Cut(model, "/"); found {
		return rest
	}

	return model
}

var modelFamilies = []struct{ keyword, family string }{
	{"claude", "Claude"},
	{"llama", "Meta Llama"},
	{"gpt-oss", "GPT OSS"},
	{"gpt", "GPT"},
	{"o1", "GPT"},
	{"o3", "GPT"},
	{"mixtral", "Mixtral"},
	{"codestral", "Mistral"},
	{"mistral", "Mistral"},
	{"gemini", "Gemini"},
	{"gemma", "Gemma"},
	{"deepseek", "DeepSeek"},
	{"qwen", "Qwen"},
	{"command", "Cohere"},
	{"cohere", "Cohere"},
	{"grok", "Grok"},
	{"nova", "Amazon Nova"},
	{"titan", "Amazon Titan"},
	{"phi", "Phi"},
	{"embed", "Embedding"},
}

func modelFamily(model string) string {
	m := strings.ToLower(bareModel(model))

	for _, f := range modelFamilies {
		if strings.Contains(m, f.keyword) {
			return f.family
		}
	}

	return "Custom Model"
}
