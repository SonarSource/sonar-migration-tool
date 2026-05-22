package summary

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// gateMappingNote is a single per-condition decision recorded by
// addGateConditions in its sidecar JSONL (addGateConditions.notes/). It
// describes either a metric remap (#143) or a dropped condition that had no
// SonarQube Cloud equivalent.
type gateMappingNote struct {
	CloudGateID   string   `json:"cloud_gate_id"`
	GateName      string   `json:"gate_name"`
	Action        string   `json:"action"` // "remapped" | "dropped"
	SourceMetric  string   `json:"source_metric"`
	TargetMetrics []string `json:"target_metrics,omitempty"`
}

// collectGateMappingNotes reads the addGateConditions.notes sidecar and
// returns one human-readable Partial-migration message per cloud gate
// affected. The key is the cloud_gate_id; values are the strings the
// summary report appends to the gate's Issues list.
func collectGateMappingNotes(runDir string) map[string][]string {
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
			// gate id). Deduplicate by (action, source, targets) so the
			// Details column shows each mapping exactly once.
			switch note.Action {
			case "remapped":
				targets := strings.Join(note.TargetMetrics, ", ")
				key := note.SourceMetric + "→" + targets
				if ag.remappedSeen[key] {
					continue
				}
				ag.remappedSeen[key] = true
				ag.remapped = append(ag.remapped,
					fmt.Sprintf("%s --> %s", note.SourceMetric, targets))
			case "dropped":
				if ag.droppedSeen[note.SourceMetric] {
					continue
				}
				ag.droppedSeen[note.SourceMetric] = true
				ag.dropped = append(ag.dropped, note.SourceMetric)
			}
		}
		f.Close()
	}

	out := make(map[string][]string, len(byGate))
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
			out[gateID] = msgs
		}
	}
	return out
}

// applyGateMappingNotes moves any Succeeded quality gate whose cloud gate id
// appears in notes into the Partial bucket, attaching the human-readable
// issue strings. Gates already in Partial get their Issues list extended.
func applyGateMappingNotes(succeeded, partial []EntityItem, notes map[string][]string) ([]EntityItem, []EntityItem) {
	if len(notes) == 0 || (len(succeeded) == 0 && len(partial) == 0) {
		return succeeded, partial
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
		issues, ok := notes[item.Detail]
		if !ok {
			keep = append(keep, item)
			continue
		}
		// Move to Partial.
		moved := EntityItem{
			Name:         item.Name,
			Language:     item.Language,
			Organization: item.Organization,
			Detail:       item.Detail,
			Issues:       append([]string(nil), issues...),
		}
		partial = append(partial, moved)
	}

	// Append notes for gates already in Partial (e.g., from another
	// upstream source).
	for gateID, issues := range notes {
		idx, ok := partialIdx[gateID]
		if !ok {
			continue
		}
		partial[idx].Issues = append(partial[idx].Issues, issues...)
	}

	return keep, partial
}

// gateMappingDataStore is an interface satisfied by *common.DataStore so the
// helper above can be exercised in unit tests without a full Executor.
type gateMappingDataStore interface {
	BaseDir() string
}

var _ gateMappingDataStore = (*common.DataStore)(nil)
