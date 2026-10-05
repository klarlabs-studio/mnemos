package capability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func full(b Binding) map[Transport]Binding {
	m := map[Transport]Binding{}
	for _, t := range Transports {
		m[t] = No("not here")
	}
	m[Go] = b
	return m
}

func TestValidate_TheRegistryIsWellFormed(t *testing.T) {
	if err := Validate(Registry); err != nil {
		t.Fatal(err)
	}
}

// The registry's rules are what make an omission explicit. Each is checked to
// fail on the shape it forbids, so a refactor of Validate cannot quietly stop
// enforcing one.
func TestValidate_RejectsEveryForbiddenShape(t *testing.T) {
	ok := Capability{ID: "x.ok", Summary: "s", Effect: Read, On: full(Has("Op"))}
	undeclared := ok
	undeclared.ID = "x.undeclared"
	undeclared.On = full(Has("Op"))
	delete(undeclared.On, REST)
	noReason := ok
	noReason.ID = "x.noreason"
	noReason.On = full(Has("Op"))
	noReason.On[GRPC] = No("")
	emptyGap := ok
	emptyGap.ID = "x.emptygap"
	emptyGap.On = full(Has("Op"))
	emptyGap.On[GRPC] = Gap("")
	both := ok
	both.ID = "x.both"
	both.On = full(Has("Op"))
	both.On[MCP] = Binding{Ops: []string{"t"}, Reason: "r"}
	unbound := ok
	unbound.ID = "x.unbound"
	unbound.On = full(No("nowhere"))
	badEffect := ok
	badEffect.ID = "x.effect"
	badEffect.Effect = "maybe"

	cases := map[string][]Capability{
		"undeclared transport":      {undeclared},
		"omission without a reason": {noReason},
		"gap without a detail":      {emptyGap},
		"ops and a reason":          {both},
		"bound nowhere":             {unbound},
		"unknown effect":            {badEffect},
		"duplicate id":              {ok, ok},
	}
	for name, caps := range cases {
		if err := Validate(caps); err == nil {
			t.Errorf("%s: Validate accepted it", name)
		}
	}
	if err := Validate([]Capability{ok}); err != nil {
		t.Errorf("a well-formed capability was rejected: %v", err)
	}
}

func TestGaps_ListsOnlyGaps(t *testing.T) {
	c := Capability{ID: "x", Summary: "s", Effect: Read, On: full(Has("Op"))}
	c.On[REST] = Gap("missing route")
	if got := Gaps([]Capability{c}); len(got) != 1 || got[0] != "x/rest" {
		t.Fatalf("Gaps = %v, want [x/rest]", got)
	}
}

// The reference is generated; this keeps the committed copy honest.
func TestRender_CommittedReferenceIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "reference", "capabilities.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run `go run ./tools/capdoc`)", path, err)
	}
	if want := Render(Registry); string(got) != want {
		t.Fatalf("%s is stale: run `go run ./tools/capdoc` and commit the result", path)
	}
	if !strings.Contains(string(got), "DO NOT EDIT") {
		t.Fatal("the reference lost its generated-file header")
	}
}
