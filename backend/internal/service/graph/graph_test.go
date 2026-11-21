package graph

import (
	"reflect"
	"strings"
	"testing"

	"flowline/internal/model"
	"flowline/internal/testutil"
)

func TestGraphValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*model.Pipeline)
		want   string
	}{
		{name: "valid"},
		{name: "cycle", change: func(p *model.Pipeline) { p.Edges = append(p.Edges, model.Edge{From: "review", To: "dev"}) }, want: "цикл"},
		{name: "missing source", change: func(p *model.Pipeline) { p.Edges[0].From = "absent" }, want: "отсутствующий блок"},
		{name: "duplicate node", change: func(p *model.Pipeline) { p.Nodes = append(p.Nodes, p.Nodes[0]) }, want: "Повторяющийся ID"},
		{name: "duplicate edge", change: func(p *model.Pipeline) { p.Edges = append(p.Edges, p.Edges[0]) }, want: "Повторяющаяся связь"},
		{name: "missing input", change: func(p *model.Pipeline) { p.Nodes[1].Prompt = "{{absent}}" }, want: "отсутствующий вход"},
		{name: "malformed token", change: func(p *model.Pipeline) { p.Nodes[1].Prompt = "{{ changes " }, want: "незакрытая"},
		{name: "nested token", change: func(p *model.Pipeline) { p.Nodes[1].Prompt = "{{ {{changes}} }}" }, want: "вложенная"},
		{name: "empty token", change: func(p *model.Pipeline) { p.Nodes[1].Prompt = "{{ }}" }, want: "некорректная ссылка"},
		{name: "reserved output", change: func(p *model.Pipeline) { p.Nodes[0].Outputs[0].Name = "branch" }, want: "зарезервированное"},
		{name: "path node id", change: func(p *model.Pipeline) { p.Nodes[0].ID = "../oops" }, want: "Некорректный ID"},
		{name: "long node id", change: func(p *model.Pipeline) { p.Nodes[0].ID = strings.Repeat("a", 101) }, want: "Некорректный ID"},
		{name: "missing executor", change: func(p *model.Pipeline) { p.Nodes[0].Executor = "" }, want: "исполнитель"},
		{name: "missing output id", change: func(p *model.Pipeline) {
			p.Nodes[1].Inputs = map[string]model.Source{"changes": {NodeID: "dev", OutputID: "absent"}}
		}, want: "отсутствующий результат"},
		{name: "binding without edge", change: func(p *model.Pipeline) {
			p.Edges = nil
			p.Nodes[1].Inputs = map[string]model.Source{"changes": {NodeID: "dev", OutputID: "changes-id"}}
		}, want: "прямую связь"},
		{name: "reserved alias", change: func(p *model.Pipeline) {
			p.Nodes[1].Inputs = map[string]model.Source{"context": {NodeID: "dev", OutputID: "changes-id"}}
		}, want: "зарезервированное имя входа"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testutil.Pipeline()
			if tt.change != nil {
				tt.change(&p)
			}

			problems := ValidateGraph(p)

			if tt.want == "" && len(problems) != 0 {
				t.Fatalf("valid graph rejected: %v", problems)
			}
			if tt.want != "" && !strings.Contains(strings.Join(problems, "\n"), tt.want) {
				t.Fatalf("wanted %q in %v", tt.want, problems)
			}
		})
	}
}

func TestAmbiguousInputsAndExplicitBindings(t *testing.T) {
	p := testutil.Pipeline()
	p.Nodes = append(p.Nodes, model.Node{ID: "other", Name: "Other", Executor: "test", Prompt: "Build", Outputs: []model.Output{{ID: "other-output", Name: "changes"}}})
	p.Edges = append(p.Edges, model.Edge{From: "other", To: "review"})

	if problems := ValidateGraph(p); !strings.Contains(strings.Join(problems, "\n"), "неоднозначная") {
		t.Fatalf("ambiguous reference accepted: %v", problems)
	}

	p.Nodes[1].Inputs = map[string]model.Source{"changes": {NodeID: "dev", OutputID: "changes-id"}}
	p.Nodes[1].Prompt = "{{changes}} and {{other.changes}}"
	if problems := ValidateGraph(p); len(problems) != 0 {
		t.Fatal(problems)
	}

	inputs, err := ResolveInputs(p, p.Nodes[1], model.GitContext{Branch: "main"}, map[string]*model.NodeRun{
		"dev":   {Status: "success", Outputs: map[string]string{"changes": "FIRST"}},
		"other": {Status: "success", Outputs: map[string]string{"changes": "SECOND"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := RenderPrompt(p.Nodes[1].Prompt, inputs)
	if err != nil || text != "FIRST and SECOND" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestResolveInputsRequiresSuccessfulResults(t *testing.T) {
	p := testutil.Pipeline()
	for _, runs := range []map[string]*model.NodeRun{
		{},
		{"dev": {Status: "error", Outputs: map[string]string{"changes": "bad"}}},
		{"dev": {Status: "success", Outputs: map[string]string{}}},
	} {
		if _, err := ResolveInputs(p, p.Nodes[1], model.GitContext{}, runs); err == nil {
			t.Fatal("accepted unavailable predecessor result")
		}
	}

	inputs, err := ResolveInputs(p, p.Nodes[1], model.GitContext{Branch: "feature", SHA: "123", Repository: model.Repository{Path: "/tmp/repo"}}, map[string]*model.NodeRun{
		"dev": {Status: "success", Outputs: map[string]string{"changes": "literal {{branch}} and $1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := RenderPrompt("{{ changes }} / {{branch}} / {{commit}}", inputs)
	if err != nil || text != "literal {{branch}} and $1 / feature / 123" {
		t.Fatalf("must substitute exactly once: %q (%v)", text, err)
	}
}

func TestRenameOutputPreservesBindingsAndWhitespace(t *testing.T) {
	p := testutil.Pipeline()
	p.Nodes[1].Inputs = map[string]model.Source{"patch": {NodeID: "dev", OutputID: "changes-id"}}
	p.Nodes[1].Prompt = "{{ changes }} {{dev.changes}} {{ patch }}"

	if err := RenameOutput(&p, "dev", "changes-id", "diff"); err != nil {
		t.Fatal(err)
	}

	if p.Nodes[0].Outputs[0] != (model.Output{ID: "changes-id", Name: "diff"}) {
		t.Fatal("output stable ID changed")
	}
	if p.Nodes[1].Prompt != "{{ diff }} {{dev.diff}} {{ patch }}" {
		t.Fatal(p.Nodes[1].Prompt)
	}
	if p.Nodes[1].Inputs["patch"] != (model.Source{NodeID: "dev", OutputID: "changes-id"}) {
		t.Fatal("stable binding changed")
	}
	if problems := ValidateGraph(p); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestRenameOutputQualifiesCollisions(t *testing.T) {
	p := testutil.Pipeline()
	p.Nodes = append(p.Nodes, model.Node{ID: "other", Name: "Other", Executor: "test", Prompt: "Build", Outputs: []model.Output{{ID: "other-output", Name: "diff"}}})
	p.Edges = append(p.Edges, model.Edge{From: "other", To: "review"})
	p.Nodes[1].Prompt += " plus {{diff}}"

	if err := RenameOutput(&p, "dev", "changes-id", "diff"); err != nil {
		t.Fatal(err)
	}

	if p.Nodes[1].Prompt != "Review {{ dev.diff }} plus {{other.diff}}" {
		t.Fatal(p.Nodes[1].Prompt)
	}
	if problems := ValidateGraph(p); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestRenameOutputDoesNotHijackAlias(t *testing.T) {
	p := testutil.Pipeline()
	p.Nodes[1].Inputs = map[string]model.Source{"changes": {NodeID: "dev", OutputID: "changes-id"}}

	if err := RenameOutput(&p, "dev", "changes-id", "diff"); err != nil {
		t.Fatal(err)
	}

	if p.Nodes[1].Prompt != "Review {{ changes }}" {
		t.Fatal("explicit alias should retain its name")
	}
}

func TestRenameOutputRejectsAmbiguousReferenceAtomically(t *testing.T) {
	p := testutil.Pipeline()
	p.Nodes = append(p.Nodes, model.Node{ID: "other", Outputs: []model.Output{{ID: "other", Name: "changes"}}})
	p.Edges = append(p.Edges, model.Edge{From: "other", To: "review"})
	before := testutil.Pipeline()
	before.Nodes = append(before.Nodes, p.Nodes[2])
	before.Edges = append(before.Edges, p.Edges[1])

	if err := RenameOutput(&p, "dev", "changes-id", "diff"); err == nil {
		t.Fatal("ambiguous shorthand must be resolved before renaming")
	}

	if !reflect.DeepEqual(p, before) {
		t.Fatal("failed rename partially mutated graph")
	}
}
