// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package scanreport

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
	"google.golang.org/protobuf/proto"
)

// ReportData holds all protobuf-built data for a single scanner report.
type ReportData struct {
	Metadata       *pb.Metadata
	RootComponent  *pb.Component
	FileComponents []*pb.Component
	Issues         map[int32][]*pb.Issue         // ref -> issues
	ExternalIssues map[int32][]*pb.ExternalIssue // ref -> external issues
	Measures       map[int32][]*pb.Measure       // ref -> measures
	Changesets     map[int32]*pb.Changesets      // ref -> changesets
	ActiveRules    []*pb.ActiveRule
	AdHocRules     []*pb.AdHocRule
	Sources        map[int32]string // ref -> source code text
	// SyntaxHighlighting maps a component ref to its ordered syntax-highlighting
	// rules. Written as syntax-highlightings-<ref>.pb so the migrated Code view
	// renders with colors instead of raw text (issue #420).
	SyntaxHighlighting map[int32][]*pb.SyntaxHighlightingRule
}

// PackageReport assembles a scanner-report.zip from the given report data.
func PackageReport(data *ReportData) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	if err := addMetadata(zw, data.Metadata); err != nil {
		return nil, err
	}
	if err := addComponents(zw, data.RootComponent, data.FileComponents); err != nil {
		return nil, err
	}
	if err := addIssues(zw, data.Issues); err != nil {
		return nil, err
	}
	if err := addExternalIssues(zw, data.ExternalIssues); err != nil {
		return nil, err
	}
	if err := addMeasures(zw, data.Measures); err != nil {
		return nil, err
	}
	if err := addChangesets(zw, data.Changesets); err != nil {
		return nil, err
	}
	if err := addActiveRules(zw, data.ActiveRules); err != nil {
		return nil, err
	}
	if err := addAdHocRules(zw, data.AdHocRules); err != nil {
		return nil, err
	}
	if err := addSources(zw, data.Sources); err != nil {
		return nil, err
	}
	if err := addSyntaxHighlighting(zw, data.SyntaxHighlighting); err != nil {
		return nil, err
	}
	if err := addEmptyContextProps(zw); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("closing zip: %w", err)
	}
	return buf.Bytes(), nil
}

func addMetadata(zw *zip.Writer, md *pb.Metadata) error {
	return addProtoMessage(zw, "metadata.pb", md)
}

func addComponents(zw *zip.Writer, root *pb.Component, files []*pb.Component) error {
	if err := addProtoMessage(zw, fmt.Sprintf("component-%d.pb", root.Ref), root); err != nil {
		return err
	}
	for _, fc := range files {
		if err := addProtoMessage(zw, fmt.Sprintf("component-%d.pb", fc.Ref), fc); err != nil {
			return err
		}
	}
	return nil
}

func addIssues(zw *zip.Writer, issues map[int32][]*pb.Issue) error {
	for ref, refIssues := range issues {
		var buf bytes.Buffer
		for _, iss := range refIssues {
			if err := writeDelimited(&buf, iss); err != nil {
				return err
			}
		}
		if err := addBytes(zw, fmt.Sprintf("issues-%d.pb", ref), buf.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

func addExternalIssues(zw *zip.Writer, extIssues map[int32][]*pb.ExternalIssue) error {
	for ref, refIssues := range extIssues {
		var buf bytes.Buffer
		for _, iss := range refIssues {
			if err := writeDelimited(&buf, iss); err != nil {
				return err
			}
		}
		if err := addBytes(zw, fmt.Sprintf("external-issues-%d.pb", ref), buf.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

func addMeasures(zw *zip.Writer, measures map[int32][]*pb.Measure) error {
	for ref, refMeasures := range measures {
		var buf bytes.Buffer
		for _, m := range refMeasures {
			if err := writeDelimited(&buf, m); err != nil {
				return err
			}
		}
		if err := addBytes(zw, fmt.Sprintf("measures-%d.pb", ref), buf.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

func addChangesets(zw *zip.Writer, changesets map[int32]*pb.Changesets) error {
	for ref, cs := range changesets {
		if err := addProtoMessage(zw, fmt.Sprintf("changesets-%d.pb", ref), cs); err != nil {
			return err
		}
	}
	return nil
}

func addActiveRules(zw *zip.Writer, rules []*pb.ActiveRule) error {
	if len(rules) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, r := range rules {
		if err := writeDelimited(&buf, r); err != nil {
			return err
		}
	}
	return addBytes(zw, "activerules.pb", buf.Bytes())
}

func addAdHocRules(zw *zip.Writer, rules []*pb.AdHocRule) error {
	if len(rules) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, r := range rules {
		if err := writeDelimited(&buf, r); err != nil {
			return err
		}
	}
	return addBytes(zw, "adhocrules.pb", buf.Bytes())
}

func addSources(zw *zip.Writer, sources map[int32]string) error {
	for ref, src := range sources {
		if err := addBytes(zw, fmt.Sprintf("source-%d.txt", ref), []byte(src)); err != nil {
			return err
		}
	}
	return nil
}

// addSyntaxHighlighting writes one syntax-highlightings-<ref>.pb per component,
// each a length-delimited stream of SyntaxHighlightingRule messages — the same
// framing the native scanner uses and CE expects. Refs with no rules are
// skipped (the scanner omits the file entirely rather than writing an empty one).
func addSyntaxHighlighting(zw *zip.Writer, highlighting map[int32][]*pb.SyntaxHighlightingRule) error {
	for ref, rules := range highlighting {
		if len(rules) == 0 {
			continue
		}
		var buf bytes.Buffer
		for _, r := range rules {
			if err := writeDelimited(&buf, r); err != nil {
				return err
			}
		}
		if err := addBytes(zw, fmt.Sprintf("syntax-highlightings-%d.pb", ref), buf.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

func addEmptyContextProps(zw *zip.Writer) error {
	return addBytes(zw, "context-props.pb", nil)
}

// addProtoMessage marshals a single proto message and adds it to the ZIP.
func addProtoMessage(zw *zip.Writer, name string, msg proto.Message) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	return addBytes(zw, name, b)
}

// writeDelimited writes a length-delimited protobuf message to a buffer.
func writeDelimited(buf *bytes.Buffer, msg proto.Message) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	// Write varint-encoded length prefix
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(b)))
	buf.Write(lenBuf[:n])
	buf.Write(b)
	return nil
}

// addBytes creates a Deflate-compressed ZIP entry using CreateHeader. Go's
// zip.Writer with CreateHeader + Deflate writes a data descriptor (bit 3),
// but Java's ZipInputStream handles data descriptors for Deflate entries.
// The earlier Store + CreateRaw approach avoided data descriptors, but
// SonarCloud CE may require Deflate-compressed entries.
func addBytes(zw *zip.Writer, name string, data []byte) error {
	if data == nil {
		data = []byte{}
	}
	fh := &zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	}
	w, err := zw.CreateHeader(fh)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}
