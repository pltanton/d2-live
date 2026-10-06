package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/golden files")

func sp(s string) *string { return &s }

func TestOps(t *testing.T) {
	fsm, err := os.ReadFile("testdata/withdraw-fsm.d2")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		src     string
		op      editOp
		wantSel string
		wantErr string
	}{
		{name: "rename", op: editOp{Kind: "rename", ID: "DECISION_APPROVED", To: "APPROVED"}, wantSel: "APPROVED"},
		{name: "rename_conflict", op: editOp{Kind: "rename", ID: "DECISION_APPROVED", To: "completed"}, wantErr: "to"},
		{name: "set_node_class", op: editOp{Kind: "set", ID: "DECISION_APPROVED", Key: "class", Value: sp("pause")}},
		{name: "set_edge_class", op: editOp{Kind: "set", ID: "(FINALIZING_COMMIT -> COMPLETED)[0]", Key: "class", Value: sp("evt")}},
		{name: "set_edge_label_new", op: editOp{Kind: "set", ID: "(DECISION_APPROVED -> FINALIZING_SEND_PENDING)[0]", Key: "label", Value: sp("go on")}},
		{name: "set_node_label_quoted", op: editOp{Kind: "set", ID: "SCREENING_AML_PENDING", Key: "label", Value: sp("waits for \"CW\"\nverdict")}},
		{name: "set_title_md", op: editOp{Kind: "set", ID: "title", Key: "label", Value: sp("# Withdraw\n\nshort **intro**")}},
		{name: "set_class_style", op: editOp{Kind: "set", ID: "classes.pause", Key: "style.fill", Value: sp("#FFF0C0")}},
		{name: "set_class_style_new_key", op: editOp{Kind: "set", ID: "classes.evt", Key: "style.font-color", Value: sp("#2B6CB0")}},
		{name: "set_style_new_key", op: editOp{Kind: "set", ID: "INTAKE_STARTED", Key: "style.fill", Value: sp("#fff")}},
		{name: "unset_class", op: editOp{Kind: "set", ID: "COMPLETED", Key: "class", Value: nil}},
		{name: "unset_nested_style", op: editOp{Kind: "set", ID: "start", Key: "style.stroke", Value: nil}},
		{name: "unset_edge_label", op: editOp{Kind: "set", ID: "(FINALIZING_SEND_PENDING -> FINALIZING_COMMIT)[0]", Key: "label", Value: nil}},
		{name: "comment_replace", op: editOp{Kind: "setComment", ID: "(INTAKE_STARTED -> REJECTED)[0]", Text: "refused hold: nothing moved"}},
		{name: "comment_insert", op: editOp{Kind: "setComment", ID: "DECISION_APPROVED", Text: "approved by AML\n\nno further checks"}},
		{name: "comment_remove", op: editOp{Kind: "setComment", ID: "(FINALIZING_CANCEL -> REJECTED)[0]", Text: ""}},
		{name: "create_node", op: editOp{Kind: "createNode", Class: "pause"}, wantSel: "NEW_STATE"},
		{name: "create_edge", op: editOp{Kind: "createEdge", Src: "FINALIZING_COMMIT", Dst: "REJECTED", Label: "commit failed", Class: "evt"}, wantSel: "(FINALIZING_COMMIT -> REJECTED)[0]"},
		{name: "create_node_with_edge", op: editOp{Kind: "createNodeWithEdge", Src: "DECISION_APPROVED", Class: "active", EdgeClass: "sync"}, wantSel: "NEW_STATE"},
		{name: "delete_node", op: editOp{Kind: "delete", ID: "DECISION_REJECTED"}},
		{name: "delete_edge", op: editOp{Kind: "delete", ID: "(INTAKE_STARTED -> REJECTED)[0]"}},
		{name: "reverse", op: editOp{Kind: "reverse", ID: "(FINALIZING_CANCEL -> REJECTED)[0]"}, wantSel: "(REJECTED -> FINALIZING_CANCEL)[0]"},
		{name: "chain_rejected", src: "a -> b -> c\n", op: editOp{Kind: "delete", ID: "b"}, wantErr: "chain"},
		{name: "implicit_node_set", src: "a -> b\n", op: editOp{Kind: "set", ID: "b", Key: "class", Value: sp("x")}},
		{name: "delete_collapses_blank", src: "a\n\n# about b\nb\n\nc\n", op: editOp{Kind: "delete", ID: "b"}},
		{name: "delete_last_trims_blank", src: "a\n\nb\n", op: editOp{Kind: "delete", ID: "b"}},
		{name: "bare_node_set", src: "a\nb\n", op: editOp{Kind: "set", ID: "a", Key: "style.fill", Value: sp("red")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.src
			if src == "" {
				src = string(fsm)
			}
			out, sel, err := applyOp(src, tc.op)
			if tc.wantErr != "" {
				var oe *opError
				if !errors.As(err, &oe) {
					t.Fatalf("want opError, got %v", err)
				}
				if oe.Field != tc.wantErr && !strings.Contains(oe.Message, tc.wantErr) {
					t.Fatalf("want error about %q, got %+v", tc.wantErr, oe)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantSel != "" && sel != tc.wantSel {
				t.Errorf("select = %q, want %q", sel, tc.wantSel)
			}
			golden := filepath.Join("testdata", "golden", tc.name+".d2")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if out != string(want) {
				t.Errorf("output differs from %s; rerun with -update and review the diff\n%s", golden, out)
			}
		})
	}
}
