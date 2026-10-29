package cmd

import (
	"context"
	"encoding/json"
	"testing"
)

type pagedAPI struct{ calls int }

func (p *pagedAPI) API(_ context.Context, method, endpoint string, body any, result any) error {
	p.calls++
	b := body.(map[string]any)
	var x map[string]any
	if p.calls == 1 {
		x = map[string]any{"object": "list", "results": []any{map[string]any{"id": "a"}}, "has_more": true, "next_cursor": "cursor"}
	} else {
		if b["start_cursor"] != "cursor" {
			panic("cursor missing")
		}
		x = map[string]any{"object": "list", "results": []any{map[string]any{"id": "b"}}, "has_more": false}
	}
	data, _ := json.Marshal(x)
	return json.Unmarshal(data, result)
}
func TestPagination(t *testing.T) {
	api := &pagedAPI{}
	items, err := fetchList(context.Background(), api, "v1/search", map[string]any{}, 10)
	if err != nil || len(items) != 2 || api.calls != 2 {
		t.Fatalf("items %v calls %d error %v", items, api.calls, err)
	}
}
func TestParseID(t *testing.T) {
	want := "2b0b955d-f763-8049-aec7-ca5dd0fd1e32"
	got, err := parseID("https://www.notion.so/a-title-2b0b955df7638049aec7ca5dd0fd1e32?pvs=4")
	if err != nil || got != want {
		t.Fatalf("%q %v", got, err)
	}
	if _, err = parseID("invalid"); err == nil {
		t.Fatal("expected error")
	}
}
