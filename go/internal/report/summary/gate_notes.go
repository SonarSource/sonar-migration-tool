package summary

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// gateConditionRef is one (metric, op, threshold) tuple as recorded in the
// sidecar JSONL. The migrator emits one for the source side and one per
// target produced by the metric mapping (composite expansions can have
// several).
type gateConditionRef struct {
	Metric string `json:"metric"`
	Op     string `json:"op"`
	Error  string `json:"error"`
}

// gateMappingNote is a single per-condition decision recorded by
// addGateConditions in its sidecar JSONL (addGateConditions.notes/). It
// describes either a metric remap (#143) or a dropped condition that had no
// SonarQube Cloud equivalent. Source + Targets carry the full conditions
// (not just metric names) so the report can render them in #143 notation.
type gateMappingNote struct {
	CloudGateID string             `json:"cloud_gate_id"`
	GateName    string             `json:"gate_name"`
	Action      string             `json:"action"` // "remapped" | "dropped"
	Source      gateConditionRef   `json:"source"`
	Targets     []gateConditionRef `json:"targets,omitempty"`
}

// gateNoteSummary captures the per-gate outcome of addGateConditions: the
// human-readable Issues lines the report renders, plus a flag indicating
// whether any source condition had to be dropped for lack of a SonarQube
// Cloud equivalent. Dropped conditions are the #227 orange criterion;
// remap-only gates qualify as #227 yellow (near-perfect).
type gateNoteSummary struct {
	Issues     []string
	HasDropped bool
}

// collectGateMappingNotes reads the addGateConditions.notes sidecar and
// returns the per-gate outcome keyed by cloud_gate_id.
func collectGateMappingNotes(runDir string) map[string]gateNoteSummary {
	dir := filepath.Join(runDir, "addGateConditions.notes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	// Aggregate per cloud_gate_id so each gate gets a tidy summary line
	// rather than one row per affected condition. The *Seen maps deduplicate
	// repeated notes (one source SQS gate mapped to a single SQC gate from
	// multiple source orgs writes the same note multiple times).
	type aggregate struct {
		remapped     []string        // human-friendly "source --> target,target"
		dropped      []string        // source metrics
		remappedSeen map[string]bool // key: source + "→" + joined targets
		droppedSeen  map[string]bool // key: source metric
	}
	byGate := make(map[string]*aggregate)

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var note gateMappingNote
			if json.Unmarshal([]byte(line), &note) != nil {
				continue
			}
			if note.CloudGateID == "" {
				continue
			}
			if byGate[note.CloudGateID] == nil {
				byGate[note.CloudGateID] = &aggregate{
					remappedSeen: map[string]bool{},
					droppedSeen:  map[string]bool{},
				}
			}
			ag := byGate[note.CloudGateID]
			// addGateConditions can record the same per-condition decision
			// more than once when the same SQS source gate is mapped to a
			// single SQC org from multiple source orgs (gates.csv carries
			// one row per source-org pairing, all sharing the same cloud
			// gate id). Deduplicate by (action, source, target) so the
			// Details column shows each mapping exactly once.
			switch note.Action {
			case "remapped":
				sourceStr := formatGateCondition(note.Source.Metric, note.Source.Op, note.Source.Error)
				// Composite mappings produce several targets; emit one
				// "source --> target" line per target so a multi-row
				// expansion is visually obvious (per #234 follow-up).
				for _, t := range note.Targets {
					targetStr := formatGateCondition(t.Metric, t.Op, t.Error)
					line := sourceStr + " --> " + targetStr
					if ag.remappedSeen[line] {
						continue
					}
					ag.remappedSeen[line] = true
					ag.remapped = append(ag.remapped, line)
				}
			case "dropped":
				sourceStr := formatGateCondition(note.Source.Metric, note.Source.Op, note.Source.Error)
				if ag.droppedSeen[sourceStr] {
					continue
				}
				ag.droppedSeen[sourceStr] = true
				ag.dropped = append(ag.dropped, sourceStr)
			}
		}
		f.Close()
	}

	out := make(map[string]gateNoteSummary, len(byGate))
	for gateID, ag := range byGate {
		var msgs []string
		if len(ag.remapped) > 0 {
			sort.Strings(ag.remapped)
			msgs = append(msgs,
				"Some metrics were mapped to the closest SonarQube Cloud equivalents:\n"+
					strings.Join(ag.remapped, "\n"))
		}
		if len(ag.dropped) > 0 {
			sort.Strings(ag.dropped)
			msgs = append(msgs,
				"Some conditions were dropped because the source metric has no SonarQube Cloud equivalent:\n"+
					strings.Join(ag.dropped, "\n"))
		}
		if len(msgs) > 0 {
			out[gateID] = gateNoteSummary{
				Issues:     msgs,
				HasDropped: len(ag.dropped) > 0,
			}
		}
	}
	return out
}

// applyGateMappingNotes routes Succeeded quality gates with addGateConditions
// notes into either NearPerfect (yellow, #227) or Partial (orange, #227):
//
//   - A gate whose only notes are "remapped" (close-equivalent metric
//     substitution per #143) lands in NearPerfect.
//   - A gate with at least one "dropped" condition — i.e. a source metric
//     with no SonarQube Cloud equivalent — lands in Partial.
//   - A gate already in Partial (e.g. set_as_default failed) keeps that
//     classification and absorbs the note Issues; orange dominates yellow.
func applyGateMappingNotes(succeeded, nearPerfect, partial []EntityItem, notes map[string]gateNoteSummary) ([]EntityItem, []EntityItem, []EntityItem) {
	if len(notes) == 0 || (len(succeeded) == 0 && len(partial) == 0) {
		return succeeded, nearPerfect, partial
	}

	// Index existing Partial entries by cloud_gate_id so we can extend
	// them rather than create duplicates.
	partialIdx := make(map[string]int, len(partial))
	for i, item := range partial {
		if item.Detail != "" {
			partialIdx[item.Detail] = i
		}
	}

	keep := succeeded[:0:0]
	for _, item := range succeeded {
		note, ok := notes[item.Detail]
		if !ok {
			keep = append(keep, item)
			continue
		}
		moved := EntityItem{
			Name:         item.Name,
			Language:     item.Language,
			Organization: item.Organization,
			Detail:       item.Detail,
			Issues:       append([]string(nil), note.Issues...),
		}
		if note.HasDropped {
			partial = append(partial, moved)
		} else {
			nearPerfect = append(nearPerfect, moved)
		}
	}

	// Append notes for gates already in Partial (e.g., set_as_default
	// failed in collectPartial). Orange dominates yellow, so we never
	// move them out — just extend Issues.
	for gateID, note := range notes {
		idx, ok := partialIdx[gateID]
		if !ok {
			continue
		}
		partial[idx].Issues = append(partial[idx].Issues, note.Issues...)
	}

	return keep, nearPerfect, partial
}

// gateMappingDataStore is an interface satisfied by *common.DataStore so the
// helper above can be exercised in unit tests without a full Executor.
type gateMappingDataStore interface {
	BaseDir() string
}

var _ gateMappingDataStore = (*common.DataStore)(nil)
