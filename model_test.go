package main

import (
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestBuildModel(t *testing.T) {
	b, err := os.ReadFile("testdata/withdraw-fsm.d2")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	m := buildModel(src)
	if m.Error != nil {
		t.Fatalf("unexpected error %+v", m.Error)
	}
	u16 := utf16.Encode([]rune(src))
	slice := func(s span) string { return string(utf16.Decode(u16[s.From:s.To])) }

	obj := map[string]objectModel{}
	for _, o := range m.Objects {
		obj[o.ID] = o
	}
	approved := obj["DECISION_APPROVED"]
	if approved.Decl == nil || slice(*approved.Decl) != "DECISION_APPROVED: {class: active}" {
		t.Fatalf("decl = %+v", approved.Decl)
	}
	if len(approved.Refs) != 2 || slice(approved.Refs[0]) != "DECISION_APPROVED" {
		t.Fatalf("refs = %+v", approved.Refs)
	}
	if approved.Props["class"] != "active" {
		t.Fatalf("props = %+v", approved.Props)
	}
	if !obj["title"].Markdown || !strings.HasPrefix(obj["title"].Label, "# Withdraw FSM") {
		t.Fatalf("title = %+v", obj["title"])
	}
	if obj["start"].Props["style.fill"] != "#334155" || obj["start"].Props["shape"] != "circle" {
		t.Fatalf("start props = %+v", obj["start"].Props)
	}

	var refused *edgeModel
	for i, e := range m.Edges {
		if e.ID == "(INTAKE_STARTED -> REJECTED)[0]" {
			refused = &m.Edges[i]
		}
	}
	if refused == nil || refused.Comment == nil || !strings.HasPrefix(refused.Comment.Text, "A refused hold") ||
		strings.Count(refused.Comment.Text, "\n") != 2 {
		t.Fatalf("edge = %+v", refused)
	}
	if refused.Props["class"] != "sync" || !strings.HasPrefix(refused.Label, "hold refused") {
		t.Fatalf("edge = %+v", refused)
	}

	var pause *classModel
	for i, c := range m.Classes {
		if c.Name == "pause" {
			pause = &m.Classes[i]
		}
	}
	if pause == nil || pause.Props["style.fill"] != "#FFF8E8" {
		t.Fatalf("classes = %+v", m.Classes)
	}
}

func TestBuildModelError(t *testing.T) {
	m := buildModel("a -> b\nc: {class: x\n")
	if m.Error == nil || m.Error.Line != 2 {
		t.Fatalf("error = %+v", m.Error)
	}
}
