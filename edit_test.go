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
	read := func(name string) string {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	fsm := read("withdraw-fsm.d2")
	nested := read("containers.d2")
	seq := read("sequence.d2")
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

		{name: "nested_rename", src: nested, op: editOp{Kind: "rename", ID: "cmp.before.api", To: "gateway"}, wantSel: "cmp.before.gateway"},
		{name: "nested_rename_sibling_conflict", src: nested, op: editOp{Kind: "rename", ID: "cmp.before.api", To: "worker"}, wantErr: "to"},
		{name: "nested_rename_other_container_ok", src: nested, op: editOp{Kind: "rename", ID: "cmp.after.worker", To: "queue"}, wantSel: "cmp.after.queue"},
		{name: "nested_set_class", src: nested, op: editOp{Kind: "set", ID: "cmp.after.worker", Key: "class", Value: sp("async")}},
		{name: "nested_create_node", src: nested, op: editOp{Kind: "createNode", Parent: "cmp.after", Class: "svc"}, wantSel: "cmp.after.NEW_STATE"},
		{name: "nested_create_edge", src: nested, op: editOp{Kind: "createEdge", Src: "cmp.after.worker", Dst: "cmp.after.api", Label: "2 · ack"}, wantSel: "cmp.after.(worker -> api)[0]"},
		{name: "nested_create_edge_across", src: nested, op: editOp{Kind: "createEdge", Src: "cmp.before.api", Dst: "cmp.after.api"}, wantSel: "cmp.(before.api -> after.api)[0]"},
		{name: "nested_node_with_edge", src: nested, op: editOp{Kind: "createNodeWithEdge", Src: "cmp.after.worker", Class: "svc", EdgeClass: "svc"}, wantSel: "cmp.after.NEW_STATE"},
		{name: "nested_delete", src: nested, op: editOp{Kind: "delete", ID: "cmp.before.queue"}},
		{name: "nested_delete_container", src: nested, op: editOp{Kind: "delete", ID: "cmp.after"}},
		{name: "nested_comment", src: nested, op: editOp{Kind: "setComment", ID: "cmp.after.(api -> worker)[0]", Text: "now a direct call"}},

		{name: "seq_rename_actor", src: seq, op: editOp{Kind: "rename", ID: "seq.server", To: "backend"}, wantSel: "seq.backend"},
		{name: "seq_rename_group", src: seq, op: editOp{Kind: "rename", ID: "seq.1 · Register", To: "1 · Create"}, wantSel: "seq.1 · Create"},
		{name: "seq_message_after", src: seq, op: editOp{Kind: "createEdge", Src: "seq.server", Dst: "seq.ledger", Label: "check limits", After: "seq.(client -> server)[0]"}, wantSel: "seq.(server -> ledger)[0]"},
		{name: "seq_message_append", src: seq, op: editOp{Kind: "createEdge", Src: "seq.client", Dst: "seq.ledger", Label: "audit"}, wantSel: "seq.(client -> ledger)[0]"},
		{name: "seq_message_append_after_groups", src: strings.Replace(seq, "\n  server -> client: \"notify\"\n", "\n", 1), op: editOp{Kind: "createEdge", Src: "seq.client", Dst: "seq.ledger", Label: "audit"}},
		{name: "seq_move_up", src: seq, op: editOp{Kind: "move", ID: "seq.(server -> client)[0]", Dir: -1}, wantSel: "seq.(server -> client)[0]"},
		{name: "seq_move_down", src: seq, op: editOp{Kind: "move", ID: "seq.(client -> server)[0]", Dir: 1}, wantSel: "seq.(client -> server)[0]"},
		{name: "seq_move_past_end", src: seq, op: editOp{Kind: "move", ID: "seq.(server -> client)[0]", Dir: 1}, wantErr: "bottom"},
		{name: "seq_create_actor", src: seq, op: editOp{Kind: "createNode", Parent: "seq", Name: "actor"}, wantSel: "seq.actor"},
		{name: "seq_delete_message", src: seq, op: editOp{Kind: "delete", ID: "seq.(ledger -> server)[1]"}},
		{name: "seq_reverse", src: seq, op: editOp{Kind: "reverse", ID: "seq.(server -> ledger)[0]"}, wantSel: "seq.(ledger -> server)[2]"}, // d2 numbers sequence messages by group, not by line
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.src
			if src == "" {
				src = fsm
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
