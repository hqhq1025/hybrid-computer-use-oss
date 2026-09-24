package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestWorkingTreeProjectionKeepsCanonicalRefsWhileCompressingLayout(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Editor\"",
			"\t\t1 section [actions=clickAncestor]",
			"\t\t\t2 panel [actions=clickAncestor]",
			"\t\t\t\t3 push button \"Save\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Editor"},
			{Index: 1, ControlType: "section", Actions: []string{"clickAncestor"}},
			{Index: 2, ControlType: "panel", Actions: []string{"clickAncestor"}},
			{Index: 3, ControlType: "push button", Name: "Save", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "1 section") || strings.Contains(text, "2 panel") {
		t.Fatalf("neutral layout wrappers leaked into working view:\n%s", text)
	}
	if !strings.Contains(text, `3 push button "Save"`) {
		t.Fatalf("actionable descendant was lost:\n%s", text)
	}
	if snapshot.serializerOmitted != 2 || len(snapshot.Elements) != 4 {
		t.Fatalf("projection changed canonical records: %#v", snapshot)
	}
	raw := rawLinesByIndex(snapshot)
	if !strings.Contains(raw[1], "clickAncestor") || raw[3] == "" {
		t.Fatalf("raw canonical lines were not retained: %#v", raw)
	}
}

func TestWorkingTreeProjectionCompactsClickCapability(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\" [has-click-action]",
			"\t\t1 section [has-click-action]",
			"\t\t\t2 heading \"Title\" [has-click-action]",
			"\t\t\t3 push button \"Continue\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page", States: "[has-click-action]"},
			{Index: 1, ControlType: "section", States: "[has-click-action]"},
			{Index: 2, ControlType: "heading", Name: "Title", States: "[has-click-action]"},
			{Index: 3, ControlType: "push button", Name: "Continue", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "1 section") {
		t.Fatalf("ubiquitous click marker kept a neutral section:\n%s", text)
	}
	if strings.Contains(linesByIndex(snapshot)[2], "click") {
		t.Fatalf("heading retained non-informative click marker:\n%s", text)
	}
	if !strings.Contains(linesByIndex(snapshot)[3], "[click]") {
		t.Fatalf("button lost compact click capability:\n%s", text)
	}
	if strings.Contains(linesByIndex(snapshot)[3], "has-click-action") {
		t.Fatalf("working view retained verbose click capability:\n%s", text)
	}
}

func TestWorkingTreeProjectionDropsEmptyNoiseButKeepsSemanticBoundaries(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Browser\"",
			"\t\t1 document web \"Page\"",
			"\t\t\t2 heading",
			"\t\t\t3 notification",
			"\t\t\t4 image [has-click-action]",
			"\t\t\t5 panel",
			"\t\t\t\t6 push button [has-click-action]",
			"\t\t\t7 menu",
			"\t\t\t8 table",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Browser"},
			{Index: 1, ControlType: "document web", Name: "Page"},
			{Index: 2, ControlType: "heading"},
			{Index: 3, ControlType: "notification"},
			{Index: 4, ControlType: "image", States: "[has-click-action]"},
			{Index: 5, ControlType: "panel"},
			{Index: 6, ControlType: "push button", States: "[has-click-action]"},
			{Index: 7, ControlType: "menu"},
			{Index: 8, ControlType: "table"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	for _, hidden := range []string{
		"2 heading",
		"3 notification",
		"4 image",
		"5 panel",
	} {
		if strings.Contains(text, hidden) {
			t.Fatalf("empty noise node %q leaked into working view:\n%s", hidden, text)
		}
	}
	for _, kept := range []string{
		`1 document web "Page"`,
		"6 push button [click]",
		"7 menu",
		"8 table",
	} {
		if !strings.Contains(text, kept) {
			t.Fatalf("semantic boundary/action %q was lost:\n%s", kept, text)
		}
	}
	if len(snapshot.Elements) != 9 || snapshot.serializerOmitted != 4 {
		t.Fatalf("working projection changed canonical records: %#v", snapshot)
	}
	if !strings.Contains(text,
		"[hidden=4 canonical=9 branches=0; find/ref/action retain hidden nodes]") {
		t.Fatalf("compact omission ledger missing:\n%s", text)
	}
}

func TestWorkingTreeProjectionRestoresDuplicateTargetBranchAncestors(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"App\"",
			"\t\t1 panel",
			"\t\t\t2 panel",
			"\t\t\t\t3 push button \"Close\" [has-click-action]",
			"\t\t4 panel",
			"\t\t\t5 panel",
			"\t\t\t\t6 push button \"Close\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "App"},
			{Index: 1, ControlType: "panel"},
			{Index: 2, ControlType: "panel"},
			{Index: 3, ControlType: "push button", Name: "Close", States: "[has-click-action]"},
			{Index: 4, ControlType: "panel"},
			{Index: 5, ControlType: "panel"},
			{Index: 6, ControlType: "push button", Name: "Close", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	for _, branch := range []string{"1 panel", "4 panel"} {
		if !strings.Contains(text, branch) {
			t.Fatalf("duplicate Close target lost branch disambiguator %q:\n%s",
				branch, text)
		}
	}
	for _, transparent := range []string{"2 panel", "5 panel"} {
		if strings.Contains(text, transparent) {
			t.Fatalf("single-chain wrapper %q should remain transparent:\n%s",
				transparent, text)
		}
	}
}

func TestWorkingTreeProjectionCompactsSmallBranchWrapperIntoGroup(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"App\"",
			"\t\t1 panel",
			"\t\t\t2 push button \"Save\" [has-click-action]",
			"\t\t\t3 push button \"Cancel\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "App"},
			{Index: 1, ControlType: "panel"},
			{Index: 2, ControlType: "push button", Name: "Save", States: "[has-click-action]"},
			{Index: 3, ControlType: "push button", Name: "Cancel", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "1 panel") {
		t.Fatalf("the raw empty layout wrapper leaked into the model view: %#v",
			snapshot.TreeLines)
	}
	if !strings.Contains(text,
		"(layout-group role=panel branches=2 hidden=1)") {
		t.Fatalf("small multi-branch hierarchy was not preserved compactly: %#v",
			snapshot.TreeLines)
	}
	if snapshot.serializerBranches != 1 || snapshot.serializerOmitted != 1 {
		t.Fatalf("unexpected compact group accounting: %#v", snapshot)
	}
}

func TestWorkingTreeProjectionDoesNotRewriteMetadataShapedUserText(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 heading \"Policy [actions=clickAncestor] [has-click-action]\" [has-click-action] [desc=\"Keep [actions=showContextMenu] and ] text\"]",
			"\t\t2 static [actions=clickAncestor]: \"Value [actions=clickAncestor] [has-click-action]\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{
				Index:       1,
				ControlType: "heading",
				Name:        "Policy [actions=clickAncestor] [has-click-action]",
				States:      "[has-click-action]",
				Description: "Keep [actions=showContextMenu] and ] text",
			},
			{
				Index:       2,
				ControlType: "static",
				Value:       "Value [actions=clickAncestor] [has-click-action]",
				Actions:     []string{"clickAncestor"},
			},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	for _, userText := range []string{
		`Policy [actions=clickAncestor] [has-click-action]`,
		`desc="Keep [actions=showContextMenu] and ] text"`,
		`Value [actions=clickAncestor] [has-click-action]`,
	} {
		if !strings.Contains(text, userText) {
			t.Fatalf("working projection rewrote quoted user text %q:\n%s", userText, text)
		}
	}
	if strings.Contains(linesByIndex(snapshot)[1], `"] [has-click-action]`) {
		t.Fatalf("heading retained the top-level click marker:\n%s", text)
	}
	if got := linesByIndex(snapshot)[2]; got !=
		`		2 static: "Value [actions=clickAncestor] [has-click-action]"` {
		t.Fatalf("static metadata cleanup changed user text or spacing: %q", got)
	}
}

func TestWorkingTreeCompactsActionsAndTableCoordinates(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Sheet\"",
			"\t\t1 table cell A1 (R0C0) Revenue [actions=edit,jump]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Sheet"},
			{
				Index:       1,
				ControlType: "table cell",
				Name:        "A1",
				Actions:     []string{"edit", "jump"},
			},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	line := linesByIndex(snapshot)[1]
	if strings.Contains(line, "(R0C0)") {
		t.Fatalf("working table line retained duplicate zero-based coordinates: %q", line)
	}
	if !strings.Contains(line, "A1") || !strings.Contains(line, "[a=edit,jump]") {
		t.Fatalf("working table line lost canonical address or actions: %q", line)
	}
	raw := rawLinesByIndex(snapshot)[1]
	if !strings.Contains(raw, "(R0C0)") ||
		!strings.Contains(raw, "[actions=edit,jump]") {
		t.Fatalf("full diagnostic line was modified: %q", raw)
	}

	record, err := lookupElement(snapshot, `table cell "A1" [a=edit]`)
	if err != nil || record.Index != 1 {
		t.Fatalf("compact action selector failed: %v %#v", err, record)
	}
	if _, err := lookupElement(snapshot, `table cell "A1" [actions=jump]`); err == nil {
		t.Fatal("legacy actions selector must be rejected")
	}
}

func TestWorkingTreeProjectionRemovesDuplicateTextContributions(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 paragraph \"Download the app ahead of your visit\"",
			"\t\t\t2 static \"Download the app\" [actions=clickAncestor]",
			"\t\t\t3 static \"ahead of your visit\" [actions=clickAncestor]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{Index: 1, ControlType: "paragraph", Name: "Download the app ahead of your visit"},
			{Index: 2, ControlType: "static", Name: "Download the app", Actions: []string{"clickAncestor"}},
			{Index: 3, ControlType: "static", Name: "ahead of your visit", Actions: []string{"clickAncestor"}},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "2 static") || strings.Contains(text, "3 static") {
		t.Fatalf("duplicate text contributions remain:\n%s", text)
	}
	if !strings.Contains(text, `1 paragraph "Download the app ahead of your visit"`) {
		t.Fatalf("aggregate text was lost:\n%s", text)
	}
}

func TestWorkingTreeProjectionDoesNotDeleteSubstringLookalikes(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 paragraph \"10 foobar\"",
			"\t\t\t2 static \"1\"",
			"\t\t\t3 static \"foo\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{Index: 1, ControlType: "paragraph", Name: "10 foobar"},
			{Index: 2, ControlType: "static", Name: "1"},
			{Index: 3, ControlType: "static", Name: "foo"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if !strings.Contains(text, `2 static "1"`) ||
		!strings.Contains(text, `3 static "foo"`) {
		t.Fatalf("substring lookalikes were incorrectly deduplicated:\n%s", text)
	}
}

func TestWorkingTreeProjectionChecksHigherAncestorsForDuplicateText(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 paragraph \"Download the app ahead of your visit\"",
			"\t\t\t2 section \"wrapper\"",
			"\t\t\t\t3 static \"Download the app\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{
				Index: 1, ControlType: "paragraph",
				Name: "Download the app ahead of your visit",
			},
			{Index: 2, ControlType: "section", Name: "wrapper"},
			{Index: 3, ControlType: "static", Name: "Download the app"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	if strings.Contains(
		strings.Join(snapshot.TreeLines, "\n"),
		`3 static "Download the app"`,
	) {
		t.Fatal("a higher ancestor's duplicate fragment should be removed")
	}
}

func TestWorkingTreeProjectionDropsReconstructableStructuralValue(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 section: \"Alpha Beta\"",
			"\t\t\t2 static \"Alpha\"",
			"\t\t\t3 static \"Beta\"",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{Index: 1, ControlType: "section", Value: "Alpha Beta"},
			{Index: 2, ControlType: "static", Name: "Alpha"},
			{Index: 3, ControlType: "static", Name: "Beta"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "1 section") {
		t.Fatalf("reconstructable structural aggregate remains:\n%s", text)
	}
	if !strings.Contains(text, `2 static "Alpha"`) ||
		!strings.Contains(text, `3 static "Beta"`) {
		t.Fatalf("leaf text was lost:\n%s", text)
	}
}

func TestWorkingTreeProjectionPreservesMeaningfulPunctuation(t *testing.T) {
	for _, test := range []struct {
		parent string
		child  string
	}{
		{parent: "-1.5", child: "15"},
		{parent: "A/B", child: "A B"},
		{parent: "foo.bar", child: "foo bar"},
	} {
		snapshot := &appSnapshot{
			TreeLines: []string{
				"\t0 frame \"Page\"",
				fmt.Sprintf("\t\t1 section: %q", test.parent),
				fmt.Sprintf("\t\t\t2 static %q", test.child),
			},
			Elements: []elementRecord{
				{Index: 0, ControlType: "frame", Name: "Page"},
				{Index: 1, ControlType: "section", Value: test.parent},
				{Index: 2, ControlType: "static", Name: test.child},
			},
		}

		applyWorkingTreeProjection(snapshot, treeViewWorking)

		if !strings.Contains(strings.Join(snapshot.TreeLines, "\n"), "1 section") {
			t.Fatalf(
				"punctuation-bearing provider value %q was treated as %q:\n%s",
				test.parent,
				test.child,
				strings.Join(snapshot.TreeLines, "\n"),
			)
		}
	}
}

func TestWorkingTreeProjectionKeepsProviderOnlyStructuralValue(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"Page\"",
			"\t\t1 section: \"Provider-only description\"",
			"\t\t\t2 push button \"Continue\" [has-click-action]",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Page"},
			{Index: 1, ControlType: "section", Value: "Provider-only description"},
			{Index: 2, ControlType: "push button", Name: "Continue", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	if !strings.Contains(strings.Join(snapshot.TreeLines, "\n"), "1 section") {
		t.Fatal("provider-only structural text must remain visible")
	}
}

func TestWorkingTreeProjectionSummarizesLargeBranchGaps(t *testing.T) {
	lines := []string{"\t0 frame \"Page\"", "\t\t1 section [actions=clickAncestor]"}
	elements := []elementRecord{
		{Index: 0, ControlType: "frame", Name: "Page"},
		{Index: 1, ControlType: "section", Actions: []string{"clickAncestor"}},
	}
	next := 2
	for branch := 0; branch < 2; branch++ {
		indent := 3
		for level := 0; level < 8; level++ {
			lines = append(lines,
				strings.Repeat("\t", indent)+fmt.Sprintf(
					"%d section [actions=clickAncestor]", next))
			elements = append(elements, elementRecord{
				Index: next, ControlType: "section",
				Actions: []string{"clickAncestor"},
			})
			next++
			indent++
		}
		lines = append(lines,
			strings.Repeat("\t", indent)+fmt.Sprintf(
				"%d push button \"Action %d\" [has-click-action]", next, branch))
		elements = append(elements, elementRecord{
			Index: next, ControlType: "push button",
			Name:   fmt.Sprintf("Action %d", branch),
			States: "[has-click-action]",
		})
		next++
	}
	snapshot := &appSnapshot{TreeLines: lines, Elements: elements}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	if !strings.Contains(text,
		"(layout-group role=section branches=2 hidden=17)") {
		t.Fatalf("large branch gap was not summarized:\n%s", text)
	}
	if snapshot.serializerBranches != 1 || snapshot.serializerOmitted != 17 {
		t.Fatalf("unexpected omission ledger: %#v", snapshot)
	}
}

func TestFullTreeViewPreservesRawProviderProjection(t *testing.T) {
	lines := []string{
		"\t0 frame \"Editor\"",
		"\t\t1 section [actions=clickAncestor]",
	}
	snapshot := &appSnapshot{
		TreeLines: append([]string(nil), lines...),
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "Editor"},
			{Index: 1, ControlType: "section", Actions: []string{"clickAncestor"}},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewFull)

	if !reflect.DeepEqual(snapshot.TreeLines, lines) ||
		snapshot.serializerOmitted != 0 {
		t.Fatalf("full view was modified: %#v", snapshot)
	}
}

func TestWorkingTreeProjectionAttachesIndentedNotesByTheirDepth(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{
			"\t0 frame \"App\"",
			"\t\t1 menu \"File\" [has-click-action]",
			"\t\t\t2 menu item \"Open\" [has-click-action]",
			"\t\t\t(3 items not listed; open the menu)",
		},
		Elements: []elementRecord{
			{Index: 0, ControlType: "frame", Name: "App"},
			{Index: 1, ControlType: "menu", Name: "File", States: "[has-click-action]"},
			{Index: 2, ControlType: "menu item", Name: "Open", States: "[has-click-action]"},
		},
	}

	applyWorkingTreeProjection(snapshot, treeViewWorking)

	text := strings.Join(snapshot.TreeLines, "\n")
	want := "\t\t\t(3 items not listed; open the menu)"
	if !strings.Contains(text, want) {
		t.Fatalf("menu note was attached to the wrong hierarchy level:\n%s", text)
	}
}

func TestWorkingProjectionSummarizesHomogeneousCollections(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{"\t0 list \"Results\""},
		Elements: []elementRecord{{
			Index: 0, RuntimeID: []int{0}, ControlType: "list", Name: "Results",
		}},
	}
	for index := 1; index <= 12; index++ {
		states := "[has-click-action]"
		if index == 8 {
			states = "[selected has-click-action]"
		}
		snapshot.TreeLines = append(
			snapshot.TreeLines,
			fmt.Sprintf("\t\t%d list item \"Item %d\" %s", index, index, states),
		)
		snapshot.Elements = append(snapshot.Elements, elementRecord{
			Index:       index,
			RuntimeID:   []int{0, index - 1},
			ControlType: "list item",
			Name:        fmt.Sprintf("Item %d", index),
			States:      states,
		})
	}
	applyWorkingTreeProjection(snapshot, treeViewWorking)
	text := strings.Join(snapshot.TreeLines, "\n")
	if !strings.Contains(text, "collection-summary role=list item items=12") {
		t.Fatalf("collection was not summarized:\n%s", text)
	}
	if !strings.Contains(text, `list item "Item 8" [selected click]`) {
		t.Fatalf("selected collection member was hidden:\n%s", text)
	}
	if len(snapshot.Elements) != 13 {
		t.Fatal("collection summary removed canonical records")
	}
	if snapshot.serializerOmitted == 0 {
		t.Fatal("collection summary did not report serializer omissions")
	}
}

func TestWorkingProjectionNormalizesQuotedWhitespace(t *testing.T) {
	record := &elementRecord{
		Index:       1,
		ControlType: "list item",
		Name:        "2024 Sedan\n  Low mileage",
		Value:       "Dealer\t\tprice",
	}
	line := "\t1 list item \"2024 Sedan\\n  Low mileage\": \"Dealer\t\tprice\""
	cleaned := cleanWorkingTreeLine(line, record)
	if cleaned != "\t1 list item \"2024 Sedan Low mileage\": \"Dealer price\"" {
		t.Fatalf("working text whitespace was not normalized: %q", cleaned)
	}
	if record.Name != "2024 Sedan\n  Low mileage" ||
		record.Value != "Dealer\t\tprice" {
		t.Fatal("working projection normalization changed canonical provider values")
	}
}

func TestWorkingProjectionSummarizesLargeStructurallyVaryingCards(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{"\t0 list \"Vehicles\""},
		Elements: []elementRecord{{
			Index: 0, RuntimeID: []int{0}, ControlType: "list", Name: "Vehicles",
		}},
	}
	for index := 1; index <= 24; index++ {
		snapshot.TreeLines = append(
			snapshot.TreeLines,
			fmt.Sprintf("\t\t%d list item \"Vehicle %d\"", index, index),
		)
		snapshot.Elements = append(snapshot.Elements, elementRecord{
			Index:       index,
			RuntimeID:   []int{0, index - 1},
			ControlType: "list item",
			Name:        fmt.Sprintf("Vehicle %d", index),
		})
		childIndex := 100 + index
		childRole := "static"
		if index%2 == 0 {
			childRole = "push button"
		}
		snapshot.TreeLines = append(
			snapshot.TreeLines,
			fmt.Sprintf("\t\t\t%d %s \"Card detail %d\"", childIndex, childRole, index),
		)
		snapshot.Elements = append(snapshot.Elements, elementRecord{
			Index:       childIndex,
			RuntimeID:   []int{0, index - 1, 0},
			ControlType: childRole,
			Name:        fmt.Sprintf("Card detail %d", index),
		})
	}
	applyWorkingTreeProjection(snapshot, treeViewWorking)
	text := strings.Join(snapshot.TreeLines, "\n")
	if count := strings.Count(
		text,
		"collection-summary role=list item items=12",
	); count != 2 {
		t.Fatalf("cards were not separated by structural shape: summaries=%d\n%s", count, text)
	}
	if len(snapshot.Elements) != 49 {
		t.Fatal("card summarization removed canonical records")
	}
}

func TestWorkingProjectionDoesNotSummarizeArbitraryLargeControlSet(t *testing.T) {
	snapshot := &appSnapshot{
		TreeLines: []string{"\t0 panel \"Commands\""},
		Elements: []elementRecord{{
			Index: 0, RuntimeID: []int{0}, ControlType: "panel", Name: "Commands",
		}},
	}
	for index := 1; index <= 24; index++ {
		snapshot.TreeLines = append(
			snapshot.TreeLines,
			fmt.Sprintf("\t\t%d push button \"Command %d\" [has-click-action]", index, index),
		)
		snapshot.Elements = append(snapshot.Elements, elementRecord{
			Index:       index,
			RuntimeID:   []int{0, index - 1},
			ControlType: "push button",
			Name:        fmt.Sprintf("Command %d", index),
			States:      "[has-click-action]",
		})
	}
	applyWorkingTreeProjection(snapshot, treeViewWorking)
	text := strings.Join(snapshot.TreeLines, "\n")
	if strings.Contains(text, "collection-summary") {
		t.Fatalf("arbitrary controls were summarized by count alone:\n%s", text)
	}
	for _, expected := range []string{`button "Command 1"`, `button "Command 12"`, `button "Command 24"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("working tree hid %q:\n%s", expected, text)
		}
	}
}
