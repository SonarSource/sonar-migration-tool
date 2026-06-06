// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sonar-solutions/sq-api-go/types"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// AI Code Fix migration (issue #251).
//
// Two SQS settings drive this whole feature:
//
//   - sonar.ai.codefix.hidden       — global flag, set at SQS startup,
//                                     hides the feature in the UI.
//   - sonar.ai.suggestions.enabled  — DISABLED / ENABLED_FOR_ALL_PROJECTS /
//                                     ENABLED_FOR_SOME_PROJECTS.
//
// The selected LLM provider + model live in the new
// getAiCodeFixConfig extract (api/v2/fix-suggestions/feature-enablements).
//
// SQC offers a strict subset of SQS's options — no self-hosted
// providers, no GPT-4o. The decision matrix below downgrades / disables
// while explaining the substitution in the Global Settings section of
// the migration report.

// AI Code Fix setting key constants — used both at peel-off time
// (extracting the SQS records) and at row-emission time (the Name
// column in the report).
const (
	AiCodeFixHiddenSetting    = "sonar.ai.codefix.hidden"
	AiCodeFixSuggestionsSetting = "sonar.ai.suggestions.enabled"
)

// aiCodeFixNearPerfectMarker is appended to outcome.Detail when the row
// should land in the NearPerfect bucket rather than Succeeded. The
// report collector strips it before display. Mirrors the existing
// project-data / NCD-fallback marker convention used for projects.
const aiCodeFixNearPerfectMarker = "|nearperfect"

// AiCodeFixNearPerfectMarker is the exported alias so the predict and
// report packages can share the same constant.
const AiCodeFixNearPerfectMarker = aiCodeFixNearPerfectMarker

// InlineBoldStart / InlineBoldEnd wrap a substring of a Detail string
// that the PDF renderer should display in bold. Private-use Unicode so
// they never collide with real data and survive sanitizeForPDF intact.
// Producers (this migrate package) emit them around the value portion
// of "Applied value=…" details; consumers (report/summary) interpret
// them via the inline-bold-aware cell renderer.
const (
	InlineBoldStart = ""
	InlineBoldEnd   = ""
)

// SQC enums — what we PATCH back to the cloud side.
const (
	sqcEnablementDisabled = "DISABLED"
	sqcEnablementAll      = "ENABLED_FOR_ALL_PROJECTS"
	sqcEnablementSome     = "ENABLED_FOR_SOME_PROJECTS"
	sqcProviderOpenAI     = "OPENAI"
	sqcProviderAnthropic  = "ANTHROPIC"
	sqcModelOpenAIGPT51   = "OPENAI_GPT_5_1"
	sqcModelClaudeSonnet4 = "CLAUDE_SONNET_4"
	// SQS-side model strings as returned by
	// api/v2/fix-suggestions/feature-enablements (the providers[].model
	// field). The format is human-readable, NOT the SQC enum form.
	sqsModelOpenAIGPT4O = "GPT-4o"
	sqsModelOpenAIGPT51 = "GPT-5.1"
)

// AiCodeFixSourceState captures the SQS-side AI Code Fix configuration
// for a single source server: the hidden flag (from getServerSettings)
// plus the parsed contents of the matching getAiCodeFixConfig extract.
// Exported so the predict pipeline can build the same struct and call
// EvaluateAiCodeFix.
type AiCodeFixSourceState struct {
	ServerURL          string
	Hidden             bool   // sonar.ai.codefix.hidden value
	Enablement         string // DISABLED / ENABLED_FOR_ALL_PROJECTS / ENABLED_FOR_SOME_PROJECTS
	EnabledProjectKeys []string
	SelectedProvider   string // type (OPENAI, ANTHROPIC, AZURE_OPENAI, ...)
	SelectedModel      string // model id (OPENAI_GPT_4O, OPENAI_GPT_5_1, ...)
	SelectedSelfHosted bool
	HasConfig          bool // false when getAiCodeFixConfig extract was missing (older SQS)
}

// AiCodeFixRowOutcome is the per-key outcome for one decision: which
// status to write, the Detail string, the optional skip reason, and
// whether the row should be promoted to NearPerfect on the report side.
type AiCodeFixRowOutcome struct {
	Status      string // outcomeApplied / outcomeSkipped / outcomeFailed
	Detail      string
	Reason      string
	NearPerfect bool // appended marker; collector promotes to NearPerfect bucket
}

// AiCodeFixDecision is the result of evaluating one SQS source against
// the #251 strategy. PatchPayload is nil when no SQC PATCH should be
// sent (e.g. when getAiCodeFixConfig is missing entirely). Suggestions
// and Hidden each carry the row outcome for the corresponding setting
// key — either may be zero-value (Status="") when the row should be
// suppressed (e.g. hidden=false stays in the default-value sweep).
type AiCodeFixDecision struct {
	ServerURL    string
	PatchPayload *types.FixSuggestionsOrgConfig
	Hidden       AiCodeFixRowOutcome // row for sonar.ai.codefix.hidden
	Suggestions  AiCodeFixRowOutcome // row for sonar.ai.suggestions.enabled
	// SourceProjectKeys is the SQS-side enabled-projects list from
	// the source extract. The migrate apply step maps these to SQC
	// cloud project keys before sending. Predict only carries them
	// to compute a "N projects" count for the Detail line.
	SourceProjectKeys []string
}

// EvaluateAiCodeFix turns a single SQS source's state into the
// per-source decision: the SQC PATCH payload to send (or nil) and the
// two row outcomes (one per setting key). Pure function — no I/O so it
// can be shared verbatim by the migrate and predict pipelines.
func EvaluateAiCodeFix(state AiCodeFixSourceState) AiCodeFixDecision {
	d := AiCodeFixDecision{ServerURL: state.ServerURL, SourceProjectKeys: state.EnabledProjectKeys}

	// Case 1 — hidden trumps everything. Per #251 both keys go Near
	// Perfect with the same comment; the PATCH disables on SQC.
	if state.Hidden {
		note := "AI Code Fix hiding is not available on SonarQube Cloud; AI Code Fix was turned off instead."
		d.PatchPayload = &types.FixSuggestionsOrgConfig{
			AiCodeFix: &types.FixSuggestionsAiCodeFix{Enablement: sqcEnablementDisabled},
		}
		d.Hidden = AiCodeFixRowOutcome{Status: outcomeApplied, Detail: note, NearPerfect: true}
		d.Suggestions = AiCodeFixRowOutcome{Status: outcomeApplied, Detail: note, NearPerfect: true}
		return d
	}

	// Cases 2–6 depend on getAiCodeFixConfig. Without it we cannot
	// pick a provider/model. Stay silent — emit no row — so older
	// SQS versions that don't expose the endpoint don't generate a
	// noisy "couldn't extract" row when AI Code Fix was never
	// configured. Operators who DO use AI Code Fix have hidden=true
	// or a customised sonar.ai.suggestions.enabled, both of which
	// land in earlier branches.
	if !state.HasConfig {
		return d
	}

	// Case 6 — explicitly disabled on SQS. Mirror to SQC.
	if state.Enablement == sqcEnablementDisabled || state.Enablement == "" {
		d.PatchPayload = &types.FixSuggestionsOrgConfig{
			AiCodeFix: &types.FixSuggestionsAiCodeFix{Enablement: sqcEnablementDisabled},
		}
		d.Suggestions = AiCodeFixRowOutcome{
			Status: outcomeApplied,
			Detail: "AI Code Fix was disabled on SonarQube Server; mirroring on SonarQube Cloud.",
		}
		return d
	}

	// Case 2 — self-hosted / private LLM. Cannot bring across; disable
	// on SQC, surface as Skipped.
	if state.SelectedSelfHosted {
		d.PatchPayload = &types.FixSuggestionsOrgConfig{
			AiCodeFix: &types.FixSuggestionsAiCodeFix{Enablement: sqcEnablementDisabled},
		}
		d.Suggestions = AiCodeFixRowOutcome{
			Status: outcomeSkipped,
			Reason: "private-llm",
			Detail: "Self-hosted / private LLM providers are not available on SonarQube Cloud; AI Code Fix was turned off by default. Reconfigure manually with a public provider (OpenAI or Anthropic) if desired.",
		}
		return d
	}

	// Cases 3, 4, 5 — public OpenAI. SQC currently supports
	// OPENAI_GPT_5_1; GPT-4o is downgraded with a Near Perfect note.
	provider := state.SelectedProvider
	model := state.SelectedModel

	sqcProviderKey := ""
	sqcModelKey := ""
	nearPerfect := false
	detail := ""

	switch provider {
	case sqcProviderOpenAI, "":
		sqcProviderKey = sqcProviderOpenAI
		switch model {
		case sqsModelOpenAIGPT4O:
			sqcModelKey = sqcModelOpenAIGPT51
			nearPerfect = true
			detail = "OpenAI GPT-4o is not available on SonarQube Cloud; the LLM was changed to GPT-5.1."
		case sqsModelOpenAIGPT51, "":
			sqcModelKey = sqcModelOpenAIGPT51
		default:
			// Unknown OpenAI model — default to GPT-5.1 and call out
			// the substitution so the report is still informative.
			sqcModelKey = sqcModelOpenAIGPT51
			nearPerfect = true
			detail = fmt.Sprintf("OpenAI model %q is not available on SonarQube Cloud; the LLM was changed to GPT-5.1.", model)
		}
	case sqcProviderAnthropic:
		sqcProviderKey = sqcProviderAnthropic
		sqcModelKey = sqcModelClaudeSonnet4
	default:
		// Non-OPENAI / non-ANTHROPIC public provider — same fallback as
		// "unknown OpenAI model" but with the provider mentioned.
		sqcProviderKey = sqcProviderOpenAI
		sqcModelKey = sqcModelOpenAIGPT51
		nearPerfect = true
		detail = fmt.Sprintf("Provider %q is not available on SonarQube Cloud; defaulting to OpenAI GPT-5.1.", provider)
	}

	// Build the PATCH payload.
	patch := &types.FixSuggestionsOrgConfig{
		AiCodeFix: &types.FixSuggestionsAiCodeFix{Enablement: state.Enablement},
		Provider:  &types.FixSuggestionsProvider{Key: sqcProviderKey, ModelKey: sqcModelKey},
	}
	if state.Enablement == sqcEnablementSome && len(state.EnabledProjectKeys) > 0 {
		// EnabledProjectKeys will be replaced with the mapped SQC keys
		// at apply time — store the SQS-side list here as a hint;
		// applyAiCodeFix overwrites before sending.
		patch.AiCodeFix.EnabledProjectKeys = append([]string(nil), state.EnabledProjectKeys...)
	}
	d.PatchPayload = patch

	// Case 5 — per-project. Append a count line to the Detail so the
	// report tells the operator how many projects are enabled.
	if state.Enablement == sqcEnablementSome {
		detail = fmt.Sprintf("%s AI Code Fix is enabled for %d project(s) on SonarQube Server.",
			strings.TrimSpace(detail), len(state.EnabledProjectKeys))
	}
	d.Suggestions = AiCodeFixRowOutcome{Status: outcomeApplied, Detail: detail, NearPerfect: nearPerfect}
	return d
}

// extractAiCodeFixHidden peels off the sonar.ai.codefix.hidden record
// from a slice of getServerSettings records, returning the record (or
// nil if absent) and the remainder. Patterned after
// extractDbCleanerBranches.
func extractAiCodeFixHidden(values []json.RawMessage) (hidden json.RawMessage, rest []json.RawMessage) {
	rest = values[:0]
	for _, raw := range values {
		if extractField(raw, "key") == AiCodeFixHiddenSetting {
			hidden = raw
			continue
		}
		rest = append(rest, raw)
	}
	return hidden, rest
}

// extractAiCodeFixSuggestions peels off the sonar.ai.suggestions.enabled
// record. The migration tool also receives this key via getServerSettings
// on newer SQS versions; the suggestion value alone isn't sufficient (we
// also need the provider/model from getAiCodeFixConfig), but peeling it
// here keeps it out of the default-value sweep so the AI Code Fix path
// owns its row.
func extractAiCodeFixSuggestions(values []json.RawMessage) (rest []json.RawMessage) {
	rest = values[:0]
	for _, raw := range values {
		if extractField(raw, "key") == AiCodeFixSuggestionsSetting {
			continue
		}
		rest = append(rest, raw)
	}
	return rest
}

// LoadAiCodeFixStates reads getServerSettings (for the hidden flag) and
// getAiCodeFixConfig (for enablement + provider + model + projects) and
// returns one AiCodeFixSourceState per source server URL. Exported so
// the predict pipeline shares the same loading logic.
func LoadAiCodeFixStates(serverSettings, aiConfigs []structure.ExtractItem) []AiCodeFixSourceState {
	hiddenByServer := make(map[string]bool)
	servers := make(map[string]struct{})
	for _, it := range serverSettings {
		servers[it.ServerURL] = struct{}{}
		if extractField(it.Data, "key") != AiCodeFixHiddenSetting {
			continue
		}
		v := extractField(it.Data, "value")
		hiddenByServer[it.ServerURL] = strings.EqualFold(v, "true")
	}
	configByServer := make(map[string]structure.ExtractItem)
	for _, it := range aiConfigs {
		servers[it.ServerURL] = struct{}{}
		configByServer[it.ServerURL] = it
	}
	urls := make([]string, 0, len(servers))
	for u := range servers {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	states := make([]AiCodeFixSourceState, 0, len(urls))
	for _, u := range urls {
		s := AiCodeFixSourceState{ServerURL: u, Hidden: hiddenByServer[u]}
		if cfg, ok := configByServer[u]; ok {
			s.HasConfig = true
			s.Enablement = extractField(cfg.Data, "enablement")
			s.EnabledProjectKeys = extractStringArray(cfg.Data, "enabledProjectKeys")
			s.SelectedProvider, s.SelectedModel, s.SelectedSelfHosted = pickSelectedProvider(cfg.Data)
		}
		states = append(states, s)
	}
	return states
}

// pickSelectedProvider walks the providers[] array in a
// getAiCodeFixConfig record and returns (type, model, selfHosted) for
// the entry flagged selected=true. Returns zero values when no entry
// is selected or the array is missing.
func pickSelectedProvider(raw json.RawMessage) (provider, model string, selfHosted bool) {
	for _, p := range extractObjectArray(raw, "providers") {
		selected, _ := p["selected"].(bool)
		if !selected {
			continue
		}
		if t, ok := p["type"].(string); ok {
			provider = t
		}
		if m, ok := p["model"].(string); ok {
			model = m
		}
		if sh, ok := p["selfHosted"].(bool); ok {
			selfHosted = sh
		}
		return
	}
	return "", "", false
}

// applyAiCodeFixDecisions applies one AiCodeFixDecision (per source SQS)
// to every target SQC org and writes the per-setting outcome rows to
// the setGlobalSettings writer. When multiple source SQS instances are
// in scope, applyAiCodeFix is called once per (source × org) but the
// emitted rows are aggregated per setting key — the user-facing report
// shows one row per (key × org), with the source's URL visible only
// when there's more than one source.
//
// The PATCH is sent only ONCE per SQC org (the decisions are
// equivalent across sources today; if they ever conflict, the first
// source wins and a follow-up note will surface in the row Detail).
func applyAiCodeFixDecisions(ctx context.Context, e *Executor,
	decisions []AiCodeFixDecision, orgList []string,
	projectKeyMap map[string]projectMapping,
	w *common.ChunkWriter, mu *sync.Mutex, counter *TaskCounter) {

	if len(decisions) == 0 || len(orgList) == 0 {
		return
	}

	// For now: one decision drives the PATCH per org (the first source
	// with a non-nil payload). The remaining sources still contribute
	// row outcomes if they say something different (e.g. one source
	// has hidden=true while another doesn't), but the SQC org will
	// receive a single PATCH per run.
	primary := pickPrimaryDecision(decisions)

	// Cache org key → UUID across the loop. The fix-suggestions
	// endpoint requires the UUID in the path; /api/organizations/search
	// on the standard sonarcloud.io base doesn't return it, so we
	// resolve via the api.sonarcloud.io GET /organizations/
	// organizations/{ref} endpoint, which accepts the key and returns
	// the full record (including id).
	uuidCache := make(map[string]string, len(orgList))

	// Build per-key outcome lists.
	hiddenRec := globalSettingResult{Key: AiCodeFixHiddenSetting}
	suggRec := globalSettingResult{Key: AiCodeFixSuggestionsSetting}

	for _, org := range orgList {
		patchOK := true
		var patchErr error
		if primary.PatchPayload != nil {
			payload := *primary.PatchPayload
			// Re-map enabledProjectKeys (SQS keys → SQC cloud_project_keys
			// scoped to THIS org).
			var droppedProjects int
			if payload.AiCodeFix != nil && len(payload.AiCodeFix.EnabledProjectKeys) > 0 {
				mapped, dropped := mapSourceProjectsToOrg(primary.SourceProjectKeys,
					primary.ServerURL, org, projectKeyMap)
				droppedProjects = dropped
				payload.AiCodeFix.EnabledProjectKeys = mapped
			}

			orgID, ok := uuidCache[org]
			if !ok {
				rec, err := e.CloudAPI.Organizations.GetByRef(ctx, org)
				if err != nil {
					patchOK = false
					patchErr = fmt.Errorf("looking up SonarQube Cloud org id: %w", err)
				} else {
					orgID = rec.ID
					uuidCache[org] = orgID
				}
			}
			if patchOK {
				if err := e.CloudAPI.FixSuggestions.PatchOrganizationConfig(ctx, orgID, payload); err != nil {
					patchOK = false
					patchErr = err
				}
			}

			if droppedProjects > 0 && patchOK {
				if primary.Suggestions.Status == outcomeApplied {
					primary.Suggestions.Detail = fmt.Sprintf("%s %d project(s) skipped — not migrated to SonarQube Cloud.",
						strings.TrimSpace(primary.Suggestions.Detail), droppedProjects)
				}
			}
		}

		appendOrgOutcome(&hiddenRec, org, primary.Hidden, patchOK, patchErr, counter)
		appendOrgOutcome(&suggRec, org, primary.Suggestions, patchOK, patchErr, counter)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hiddenRec.Outcomes) > 0 {
		b, _ := json.Marshal(hiddenRec)
		_ = w.WriteOne(b)
	}
	if len(suggRec.Outcomes) > 0 {
		b, _ := json.Marshal(suggRec)
		_ = w.WriteOne(b)
	}
}

// appendOrgOutcome materialises one per-org orgOutcome on the given
// record, honouring the per-source row outcome plus the PATCH success
// flag. When the row outcome is empty (Status==""), nothing is
// emitted — keeps the hidden row out of the report in the common
// hidden=false case.
func appendOrgOutcome(rec *globalSettingResult, org string,
	row AiCodeFixRowOutcome, patchOK bool, patchErr error,
	counter *TaskCounter) {

	if row.Status == "" {
		return
	}
	if !patchOK && (row.Status == outcomeApplied) {
		counter.Fail()
		rec.Outcomes = append(rec.Outcomes, orgOutcome{
			Org: org, Status: outcomeFailed,
			Reason: patchErrReason(patchErr),
			Detail: fmt.Sprintf("Failed to update AI Code Fix on SonarQube Cloud: %s", apiErrMessage(patchErr)),
		})
		return
	}

	detail := row.Detail
	if row.NearPerfect {
		detail += aiCodeFixNearPerfectMarker
	}
	switch row.Status {
	case outcomeApplied:
		counter.Success()
	}
	rec.Outcomes = append(rec.Outcomes, orgOutcome{
		Org:    org,
		Status: row.Status,
		Detail: detail,
		Reason: row.Reason,
	})
}

func patchErrReason(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// pickPrimaryDecision returns the first decision with a non-nil PATCH
// payload — used to seed the per-org SQC PATCH. Falls back to the first
// decision in the slice when none has a payload.
func pickPrimaryDecision(decisions []AiCodeFixDecision) AiCodeFixDecision {
	for _, d := range decisions {
		if d.PatchPayload != nil {
			return d
		}
	}
	return decisions[0]
}

// mapSourceProjectsToOrg translates a list of SQS project keys into SQC
// cloud_project_keys scoped to the given target org. Projects that
// don't resolve (skipped from migration, wrong source server, or wrong
// target org) are counted in dropped — the apply step folds the count
// into the Detail string.
func mapSourceProjectsToOrg(srcKeys []string, srcServerURL, targetOrg string,
	projectKeyMap map[string]projectMapping) (mapped []string, dropped int) {

	for _, k := range srcKeys {
		pm, ok := projectKeyMap[srcServerURL+k]
		if !ok || pm.OrgKey != targetOrg || pm.CloudKey == "" {
			dropped++
			continue
		}
		mapped = append(mapped, pm.CloudKey)
	}
	return mapped, dropped
}
