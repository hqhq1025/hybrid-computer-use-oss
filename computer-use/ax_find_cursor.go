package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const defaultAXFindCursorLimit = 32
const maxAXFindPageSize = 50
const defaultAXFindCursorNodeBudget = 50000
const defaultAXFindCursorByteBudget = 64 << 20
const defaultAXFindCursorIdleTTL = 2 * time.Minute
const defaultAXFindCursorAbsoluteTTL = 10 * time.Minute

type AXFindCursorEntry struct {
	Ref          string
	ResultSetID  string
	Offset       int
	LastUsedTick uint64
}

type AXFindResultSet struct {
	ID           string
	Snapshot     *appSnapshot
	Matches      []elementRecord
	Query        string
	LastUsedTick uint64
	CreatedAt    time.Time
	LastUsedAt   time.Time
	NodeCount    int
	ApproxBytes  int
}

type AXFindCursorResolution struct {
	Cursor    AXFindCursorEntry
	ResultSet *AXFindResultSet
}

type FindCursorRegistry struct {
	nextCursor  uint64
	nextSet     uint64
	tick        uint64
	limit       int
	nodeBudget  int
	byteBudget  int
	idleTTL     time.Duration
	absoluteTTL time.Duration
	entries     map[string]*AXFindCursorEntry
	resultSets  map[string]*AXFindResultSet
}

func NewFindCursorRegistry(limit int) *FindCursorRegistry {
	if limit <= 0 {
		limit = defaultAXFindCursorLimit
	}
	return &FindCursorRegistry{
		nextCursor:  1,
		nextSet:     1,
		limit:       limit,
		nodeBudget:  defaultAXFindCursorNodeBudget,
		byteBudget:  defaultAXFindCursorByteBudget,
		idleTTL:     defaultAXFindCursorIdleTTL,
		absoluteTTL: defaultAXFindCursorAbsoluteTTL,
		entries:     map[string]*AXFindCursorEntry{},
		resultSets:  map[string]*AXFindResultSet{},
	}
}

func parseAXFindCursor(ref string) (uint64, bool) {
	ref = strings.TrimSpace(ref)
	if len(ref) < 2 || ref[0] != 'f' {
		return 0, false
	}
	value, err := strconv.ParseUint(ref[1:], 10, 64)
	return value, err == nil && value > 0
}

func (registry *FindCursorRegistry) Create(
	snapshot *appSnapshot,
	matches []elementRecord,
	offset int,
	query string,
) string {
	if registry == nil || snapshot == nil || offset < 0 || offset >= len(matches) {
		return ""
	}
	now := time.Now()
	registry.pruneExpired(now)
	compactSnapshot := compactFindSnapshot(snapshot)
	nodeCount, approxBytes := estimateFindResultSet(compactSnapshot, matches)
	if nodeCount > registry.nodeBudget || approxBytes > registry.byteBudget {
		return ""
	}
	registry.tick++
	setID := "fs" + strconv.FormatUint(registry.nextSet, 10)
	registry.nextSet++
	registry.resultSets[setID] = &AXFindResultSet{
		ID:           setID,
		Snapshot:     compactSnapshot,
		Matches:      append([]elementRecord(nil), matches...),
		Query:        query,
		LastUsedTick: registry.tick,
		CreatedAt:    now,
		LastUsedAt:   now,
		NodeCount:    nodeCount,
		ApproxBytes:  approxBytes,
	}
	ref := registry.createCursor(setID, offset)
	registry.evict()
	return ref
}

func (registry *FindCursorRegistry) createCursor(
	resultSetID string,
	offset int,
) string {
	if registry == nil || registry.resultSets[resultSetID] == nil || offset < 0 {
		return ""
	}
	registry.tick++
	ref := "f" + strconv.FormatUint(registry.nextCursor, 10)
	registry.nextCursor++
	registry.entries[ref] = &AXFindCursorEntry{
		Ref:          ref,
		ResultSetID:  resultSetID,
		Offset:       offset,
		LastUsedTick: registry.tick,
	}
	return ref
}

func (registry *FindCursorRegistry) Continue(
	resultSetID string,
	offset int,
) string {
	registry.pruneExpired(time.Now())
	ref := registry.createCursor(resultSetID, offset)
	registry.evict()
	return ref
}

func (registry *FindCursorRegistry) Resolve(
	ref string,
) (*AXFindCursorResolution, error) {
	id, ok := parseAXFindCursor(ref)
	if !ok {
		return nil, fmt.Errorf("invalid AX find cursor %q", ref)
	}
	if registry == nil {
		return nil, errors.New("AX find cursor registry is unavailable")
	}
	registry.pruneExpired(time.Now())
	entry := registry.entries[strings.TrimSpace(ref)]
	if entry == nil {
		if id < registry.nextCursor {
			return nil, fmt.Errorf(
				"AX find cursor %q expired; run AX mode=find again",
				ref,
			)
		}
		return nil, fmt.Errorf("unknown AX find cursor %q", ref)
	}
	resultSet := registry.resultSets[entry.ResultSetID]
	if resultSet == nil {
		delete(registry.entries, entry.Ref)
		return nil, fmt.Errorf(
			"AX find cursor %q expired; run AX mode=find again",
			ref,
		)
	}
	registry.tick++
	entry.LastUsedTick = registry.tick
	resultSet.LastUsedTick = registry.tick
	resultSet.LastUsedAt = time.Now()
	return &AXFindCursorResolution{
		Cursor:    *entry,
		ResultSet: resultSet,
	}, nil
}

func (registry *FindCursorRegistry) Reset() {
	if registry == nil {
		return
	}
	registry.entries = map[string]*AXFindCursorEntry{}
	registry.resultSets = map[string]*AXFindResultSet{}
}

func (registry *FindCursorRegistry) evict() {
	for registry != nil && len(registry.entries) > registry.limit {
		oldestRef := ""
		var oldestTick uint64
		for ref, entry := range registry.entries {
			if oldestRef == "" || entry.LastUsedTick < oldestTick {
				oldestRef = ref
				oldestTick = entry.LastUsedTick
			}
		}
		delete(registry.entries, oldestRef)
	}
	for registry != nil &&
		(len(registry.resultSets) > registry.limit ||
			registry.totalResultSetNodes() > registry.nodeBudget ||
			registry.totalResultSetBytes() > registry.byteBudget) {
		oldestID := ""
		var oldestTick uint64
		for id, resultSet := range registry.resultSets {
			if oldestID == "" || resultSet.LastUsedTick < oldestTick {
				oldestID = id
				oldestTick = resultSet.LastUsedTick
			}
		}
		registry.removeResultSet(oldestID)
	}
}

func (registry *FindCursorRegistry) pruneExpired(now time.Time) {
	if registry == nil {
		return
	}
	for id, resultSet := range registry.resultSets {
		idleExpired := registry.idleTTL > 0 &&
			now.Sub(resultSet.LastUsedAt) > registry.idleTTL
		absoluteExpired := registry.absoluteTTL > 0 &&
			now.Sub(resultSet.CreatedAt) > registry.absoluteTTL
		if idleExpired || absoluteExpired {
			registry.removeResultSet(id)
		}
	}
}

func (registry *FindCursorRegistry) removeResultSet(id string) {
	if registry == nil {
		return
	}
	delete(registry.resultSets, id)
	for ref, entry := range registry.entries {
		if entry.ResultSetID == id {
			delete(registry.entries, ref)
		}
	}
}

func (registry *FindCursorRegistry) totalResultSetNodes() int {
	total := 0
	for _, resultSet := range registry.resultSets {
		total += resultSet.NodeCount
	}
	return total
}

func (registry *FindCursorRegistry) totalResultSetBytes() int {
	total := 0
	for _, resultSet := range registry.resultSets {
		total += resultSet.ApproxBytes
	}
	return total
}

func compactFindSnapshot(snapshot *appSnapshot) *appSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	clone.ScreenshotPNGBase64 = ""
	clone.ScreenshotMimeType = ""
	clone.ScreenshotEncoding = ""
	clone.TreeLines = nil
	clone.rawTreeLines = nil
	clone.baselineProjectionLines = nil
	clone.Elements = nil
	clone.Refs = nil
	clone.Annotations = nil
	clone.DesktopLayout = nil
	clone.DocumentState = nil
	if snapshot.axSnapshot != nil {
		axClone := *snapshot.axSnapshot
		axClone.CanonicalRoots = cloneAXForest(
			snapshot.axSnapshot.CanonicalRoots, nil,
		)
		axClone.Roots = axClone.CanonicalRoots
		indexAXSnapshot(&axClone)
		var stripSourceRecords func([]*AXNode)
		stripSourceRecords = func(nodes []*AXNode) {
			for _, node := range nodes {
				if node == nil {
					continue
				}
				node.SourceRecord = nil
				stripSourceRecords(node.Children)
			}
		}
		stripSourceRecords(axClone.CanonicalRoots)
		clone.axSnapshot = &axClone
	}
	return &clone
}

func estimateFindResultSet(
	snapshot *appSnapshot,
	matches []elementRecord,
) (nodes, bytes int) {
	if snapshot != nil && snapshot.axSnapshot != nil {
		var visit func([]*AXNode)
		visit = func(items []*AXNode) {
			for _, node := range items {
				if node == nil {
					continue
				}
				nodes++
				bytes += 256 + len(node.ID) + len(node.Name) +
					len(node.ProviderRole) + len(node.Properties.Description) +
					len(node.Properties.Value) + len(node.Properties.Placeholder)
				visit(node.Children)
			}
		}
		visit(snapshot.axSnapshot.CanonicalRoots)
	}
	for _, match := range matches {
		bytes += 256 + len(match.Name) + len(match.Value) +
			len(match.Description) + len(match.Placeholder) +
			len(match.ControlType) + len(match.ProviderObjectPath)
	}
	if encoded, err := json.Marshal(struct {
		Snapshot *AXSnapshotV1   `json:"snapshot"`
		Matches  []elementRecord `json:"matches"`
	}{
		Snapshot: func() *AXSnapshotV1 {
			if snapshot == nil {
				return nil
			}
			return snapshot.axSnapshot
		}(),
		Matches: matches,
	}); err == nil && len(encoded) > bytes {
		bytes = len(encoded)
	}
	return nodes, bytes
}
