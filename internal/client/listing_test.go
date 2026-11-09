package client

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/jarcoal/httpmock"
)

type testItem struct {
	ID string `json:"groupId"`
}

func TestListAllFollowsPages(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	const total = 205
	var seen []string
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/groups", func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		seen = append(seen, q.Get("offset")+"/"+q.Get("limit"))
		if q.Get("labels") != "env:prod" {
			t.Errorf("caller query lost: %q", r.URL.RawQuery)
		}
		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		items := []testItem{}
		for i := offset; i < offset+limit && i < total; i++ {
			items = append(items, testItem{ID: "g" + strconv.Itoa(i)})
		}
		// 3.3 may skip count; the client must not rely on it.
		return httpmock.NewJsonResponse(200, map[string]any{"groups": items})
	})
	got, err := listAll[testItem](context.Background(), c, "/groups", map[string][]string{"labels": {"env:prod"}}, "groups", "list groups")
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(got) != total {
		t.Fatalf("got %d items, want %d", len(got), total)
	}
	if got[0].ID != "g0" || got[total-1].ID != "g204" {
		t.Errorf("items out of order: first %q last %q", got[0].ID, got[total-1].ID)
	}
	want := []string{"0/100", "100/100", "200/100"}
	if len(seen) != len(want) {
		t.Fatalf("pages requested %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("page %d = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestListAllEmptyAndMissingKey(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/groups", httpmock.NewStringResponder(200, `{"count":0}`))
	got, err := listAll[testItem](context.Background(), c, "/groups", nil, "groups", "list groups")
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d items, want 0", len(got))
	}
}

func TestListAllPropagatesErrors(t *testing.T) {
	t.Parallel()
	c, mt := newTestClient(t)
	mt.RegisterResponder(http.MethodGet, testURL+APIPath+"/groups/g/artifacts",
		httpmock.NewStringResponder(404, problem(404, "GroupNotFoundException", "No group 'g' was found.")))
	_, err := listAll[testItem](context.Background(), c, "/groups/g/artifacts", nil, "artifacts", "list artifacts")
	if !IsNotFound(err) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
